#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# Create one fresh GNU Wget 1.21.4 source variant for the TargetCheck example.
#
#   sh examples/wget/make-variant.sh VARIANT TARBALL DESTINATION
#
# VARIANT is unpatched, first-fix or complete (see series/). TARBALL is the
# pinned wget-1.21.4.tar.gz; DESTINATION must not exist. Nothing is fetched.
# The tarball and every patch are checked against SHA256SUMS before use, each
# patch is applied with patch -p1 --fuzz=0, and the resulting sources must
# match the reviewed hashes in SHA256SUMS. The variant records its own source
# identity in TARGETCHECK-VARIANT.txt and TARGETCHECK-SOURCE.sha256, which the
# committed manifests declare as TargetCheck inputs. Requires a Linux userland
# (GNU or compatible tar, patch, sha256sum, find, sort, xargs).
set -eu

fail() {
	printf 'make-variant: %s\n' "$*" >&2
	exit 1
}

[ "$#" -eq 3 ] || fail "usage: make-variant.sh VARIANT TARBALL DESTINATION"
variant=$1
tarball=$2
destination=$3
recipe=$(cd "$(dirname "$0")" && pwd -P)
sums=$recipe/SHA256SUMS
series=$recipe/series/$variant
patch_flags='-p1 --fuzz=0 --forward --no-backup-if-mismatch --reject-file=-'

case $variant in
unpatched | first-fix | complete) ;;
*) fail "unknown variant '$variant' (expected unpatched, first-fix or complete)" ;;
esac
for tool in tar gzip patch sha256sum find sort xargs; do
	command -v "$tool" >/dev/null 2>&1 || fail "required tool '$tool' is not installed"
done
[ -f "$series" ] || fail "missing series file $series"
[ -f "$tarball" ] || fail "missing source tarball $tarball (run make prepare-demo)"
[ ! -e "$destination" ] || fail "destination $destination already exists; variants are always created fresh"

# expected NAME prints the reviewed SHA-256 for NAME from SHA256SUMS.
expected() {
	value=$(awk -v name="$1" '$1 !~ /^#/ && $2 == name { print $1 }' "$sums")
	[ -n "$value" ] || fail "SHA256SUMS has no entry for $1"
	printf '%s\n' "$value"
}
digest() {
	sha256sum "$1" | cut -d ' ' -f 1
}
check() { # check NAME FILE
	want=$(expected "$1")
	got=$(digest "$2")
	[ "$got" = "$want" ] || fail "SHA-256 mismatch for $1: expected $want, got $got"
}

# Verify every input before extraction, so a corrupted or substituted source
# is refused without creating the destination.
check wget-1.21.4.tar.gz "$tarball"
patches=$(grep -v '^#' "$series")
for p in $patches; do
	check "patches/$p" "$recipe/patches/$p"
done

parent=$(dirname "$destination")
[ -d "$parent" ] || fail "destination parent $parent does not exist"
work=$(mktemp -d "$parent/.make-variant.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
tar -xzf "$tarball" -C "$work"
[ -d "$work/wget-1.21.4" ] || fail "tarball does not contain wget-1.21.4/"
tree=$work/wget-1.21.4
record=$work/TARGETCHECK-VARIANT.txt
{
	printf 'TargetCheck Wget example variant: %s\n' "$variant"
	printf 'Source: https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz\n'
	printf 'Source SHA-256: %s\n' "$(expected wget-1.21.4.tar.gz)"
	printf 'Patch tool: %s\n' "$(patch --version | head -n 1)"
	printf 'Patch command: patch %s\n' "$patch_flags"
} >"$record"
n=0
for p in $patches; do
	n=$((n + 1))
	# shellcheck disable=SC2086 # patch_flags is a fixed list of words.
	if ! output=$(cd "$tree" && patch $patch_flags <"$recipe/patches/$p" 2>&1); then
		printf '%s\n' "$output" >&2
		fail "patch $p does not apply to $variant with --fuzz=0"
	fi
	case $output in
	*fuzz* | *FAILED* | *reversed* | *Reversed*) printf '%s\n' "$output" >&2; fail "patch $p did not apply exactly" ;;
	esac
	{
		printf 'Patch %d: %s sha256=%s\n' "$n" "$p" "$(digest "$recipe/patches/$p")"
		printf '%s\n' "$output" | sed 's/^/  /'
	} >>"$record"
done
if leftovers=$(cd "$tree" && find . \( -name '*.orig' -o -name '*.rej' \) -print) && [ -n "$leftovers" ]; then
	fail "patching left backup or reject files: $leftovers"
fi

# Source identity of the complete tree before any build.
(cd "$tree" && find . -type f ! -name 'TARGETCHECK-*' -print | sed 's|^\./||' | LC_ALL=C sort | tr '\n' '\0' | xargs -0 sha256sum) >"$work/TARGETCHECK-SOURCE.sha256"
mv "$work/TARGETCHECK-SOURCE.sha256" "$tree/TARGETCHECK-SOURCE.sha256"
files=$(wc -l <"$tree/TARGETCHECK-SOURCE.sha256" | tr -d ' ')
{
	printf 'Source files: %s (listed in TARGETCHECK-SOURCE.sha256)\n' "$files"
	printf 'TARGETCHECK-SOURCE.sha256 sha256=%s\n' "$(digest "$tree/TARGETCHECK-SOURCE.sha256")"
	for f in src/http.c tests/content-range-cases.c tests/unit-tests.c tests/unit-tests.h; do
		printf '%s sha256=%s\n' "$f" "$(digest "$tree/$f")"
	done
} >>"$record"
mv "$record" "$tree/TARGETCHECK-VARIANT.txt"

# The resulting sources must be exactly the reviewed variant. The complete
# file list covers every source file; the named files are checked directly.
for f in TARGETCHECK-SOURCE.sha256 src/http.c tests/content-range-cases.c tests/unit-tests.c tests/unit-tests.h; do
	check "$variant/$f" "$tree/$f"
done
mv "$tree" "$destination"
printf 'make-variant: created %s variant at %s (%s source files, sources verified)\n' "$variant" "$destination" "$files"
