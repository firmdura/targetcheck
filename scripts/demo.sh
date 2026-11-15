#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# Run and assert the mandatory real Wget CVE-2026-58470 scenario matrix
# offline (make demo). Requires the pinned Linux environment and a cache
# prepared by make prepare-demo; nothing is downloaded.
#
# The script
#   1. checks the required tools and re-verifies the cached tarball;
#   2. proves that a corrupted tarball copy is refused before extraction;
#   3. builds the TargetCheck CLI and the demo checker;
#   4. twice, in fresh directories: creates every source variant from a
#      verified private tarball copy (examples/wget/make-variant.sh), runs
#      `targetcheck check` for every committed manifest in a minimal
#      environment, and asserts exits, named outcomes, diagnostics, digests
#      and ELF identities (examples/wget/democheck);
#   5. requires the two normalized results to be identical.
#
# Evidence stays in a new directory below DEMO_DIR (default .cache/wget-demo).
set -eu

repo=$(pwd -P)
recipe=$repo/examples/wget
cache=${WGET_CACHE_DIR:-$repo/.cache/wget}
tarball=$cache/wget-1.21.4.tar.gz
parent=${DEMO_DIR:-$repo/.cache/wget-demo}
sysroot=/usr/arm-linux-gnueabihf
go=${GO:-go}

# Row, baseline variant and candidate variant for each committed manifest
# that executes; the preflight-failure manifests reuse b-arm-large-offset-first-fix.
rows='a-native-security:unpatched:complete
b-native-large-offset-first-fix:unpatched:first-fix
b-arm-large-offset-first-fix:unpatched:first-fix
b-arm-large-offset-corrected:first-fix:complete
c-arm-security:unpatched:complete'
preflight_rows='missing-qemu missing-sysroot'

fail() {
	printf 'demo: %s\n' "$*" >&2
	exit 1
}

[ "$(uname -s)" = Linux ] || fail "Linux is required; on macOS use the pinned container: make container-build prepare-demo demo-container"
[ -f "$recipe/matrix.json" ] || fail "run from the repository root (make demo)"
missing=
for tool in "$go" gofmt gcc make patch tar gzip sha256sum find sort xargs dd od cmp diff readlink arm-linux-gnueabihf-gcc qemu-arm; do
	command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
for file in lib/ld-linux-armhf.so.3 lib/libc.so.6 lib/libubsan.so.1; do
	[ -f "$sysroot/$file" ] || missing="$missing $sysroot/$file"
done
[ -z "$missing" ] || fail "missing required tools or ARM sysroot files:$missing. Nothing was skipped; install them or use the pinned container (make container-build demo-container)."

# Refuse a missing or corrupted cache before anything is extracted.
WGET_CACHE_DIR=$cache sh "$repo/scripts/prepare-demo.sh" --verify-only

mkdir -p "$parent"
demo=$(mktemp -d "$parent/demo.XXXXXX")
demo=$(cd "$demo" && pwd -P)
printf 'demo: evidence directory %s\n' "$demo"

# Negative preparation path: a corrupted copy is refused by both the
# preparation check and variant creation, and nothing is extracted.
bad=$demo/corrupted-cache
mkdir "$bad"
cp "$tarball" "$bad/wget-1.21.4.tar.gz"
byte=$(od -An -tu1 -j 1000000 -N 1 "$bad/wget-1.21.4.tar.gz" | tr -d ' ')
# shellcheck disable=SC2059 # the format is a generated octal escape.
printf "\\$(printf '%03o' $(((byte + 1) % 256)))" | dd of="$bad/wget-1.21.4.tar.gz" bs=1 seek=1000000 conv=notrunc 2>/dev/null
! cmp -s "$tarball" "$bad/wget-1.21.4.tar.gz" || fail "could not corrupt the tarball copy"
if WGET_CACHE_DIR=$bad sh "$repo/scripts/prepare-demo.sh" --verify-only >"$bad/prepare-demo.log" 2>&1; then
	fail "prepare-demo accepted a corrupted tarball"
fi
grep -q 'refusing .* does not match the pinned' "$bad/prepare-demo.log" || fail "prepare-demo did not explain the refusal: $(cat "$bad/prepare-demo.log")"
if sh "$recipe/make-variant.sh" unpatched "$bad/wget-1.21.4.tar.gz" "$bad/unpatched" >"$bad/make-variant.log" 2>&1; then
	fail "make-variant accepted a corrupted tarball"
fi
grep -q 'SHA-256 mismatch for wget-1.21.4.tar.gz' "$bad/make-variant.log" || fail "make-variant did not explain the refusal: $(cat "$bad/make-variant.log")"
leftover=$(find "$bad" -mindepth 1 ! -name wget-1.21.4.tar.gz ! -name '*.log' -print)
[ -z "$leftover" ] || fail "a corrupted tarball left extracted files: $leftover"
printf 'demo: corrupted tarball refused before extraction (%s)\n' "$(cat "$bad/prepare-demo.log")"

mkdir "$demo/bin"
# Build without Go's VCS stamping: git refuses a bind-mounted checkout owned
# by another user, which would fail the build. The reports record a commit
# only when it is known to describe these sources: TARGETCHECK_COMMIT (set by
# make demo-container to the archived HEAD) or the HEAD of a clean checkout
# that git can read. Otherwise the reports honestly say "unknown".
commit=
commit_source='none (no TARGETCHECK_COMMIT and no clean git checkout); reports say unknown'
if [ -n "${TARGETCHECK_COMMIT:-}" ]; then
	case $TARGETCHECK_COMMIT in
	*[!0-9a-f]*) fail "TARGETCHECK_COMMIT must be a lowercase hexadecimal commit ID" ;;
	esac
	[ "${#TARGETCHECK_COMMIT}" -eq 40 ] || fail "TARGETCHECK_COMMIT must have 40 hexadecimal digits"
	commit=$TARGETCHECK_COMMIT
	commit_source='TARGETCHECK_COMMIT'
elif command -v git >/dev/null 2>&1 && git_head=$(git rev-parse --verify -q HEAD 2>/dev/null) &&
	changes=$(git status --porcelain 2>/dev/null) && [ -z "$changes" ]; then
	commit=$git_head
	commit_source='clean git checkout HEAD'
fi
ldflags=
[ -z "$commit" ] || ldflags="-X github.com/firmdura/targetcheck/internal/buildinfo.Commit=$commit"
"$go" build -buildvcs=false -trimpath -ldflags "$ldflags" -o "$demo/bin/targetcheck" ./cmd/targetcheck
# The checker is excluded from ./... by a build constraint; vet it here.
unformatted=$(gofmt -l "$recipe/democheck")
[ -z "$unformatted" ] || fail "run gofmt on: $unformatted"
"$go" vet "$recipe/democheck/main.go"
"$go" build -buildvcs=false -trimpath -o "$demo/bin/democheck" "$recipe/democheck/main.go"
targetcheck=$demo/bin/targetcheck

# Tool versions, resolved paths and binary digests. These are observations
# of this environment, not an attestation of it.
tool_line() { # LABEL PATH VERSION...
	label=$1
	path=$2
	shift 2
	real=$(readlink -f "$path")
	printf '%s: %s -> %s sha256=%s\n  %s\n' "$label" "$path" "$real" "$(sha256sum "$real" | cut -d ' ' -f 1)" "$*"
}
{
	printf 'TargetCheck Wget demo environment (offline observations)\n'
	printf 'uname: %s\n' "$(uname -srm)"
	printf 'targetcheck: %s\n' "$("$targetcheck" version)"
	printf 'targetcheck commit source: %s\n' "$commit_source"
	printf 'go: %s\n' "$("$go" version)"
	tool_line gcc "$(command -v gcc)" "$(gcc --version | head -n 1)"
	tool_line cc1 "$(gcc -print-prog-name=cc1)" "(native compiler proper)"
	tool_line arm-linux-gnueabihf-gcc "$(command -v arm-linux-gnueabihf-gcc)" "$(arm-linux-gnueabihf-gcc --version | head -n 1)"
	tool_line arm-cc1 "$(arm-linux-gnueabihf-gcc -print-prog-name=cc1)" "(ARM compiler proper)"
	tool_line qemu-arm "$(command -v qemu-arm)" "$(qemu-arm --version | head -n 1)"
	tool_line patch "$(command -v patch)" "$(patch --version | head -n 1)"
	tool_line make "$(command -v make)" "$(make --version | head -n 1)"
	tool_line native-libubsan "$(gcc -print-file-name=libubsan.so)" "(native UBSan runtime)"
	for file in lib/ld-linux-armhf.so.3 lib/libc.so.6 lib/libubsan.so.1; do
		tool_line "sysroot $file" "$sysroot/$file" "(ARM sysroot runtime)"
	done
	if command -v dpkg-query >/dev/null 2>&1; then
		printf 'dpkg-query:\n'
		dpkg-query -W -f '  ${Package}:${Architecture}=${Version}\n' gcc-12 libubsan1 gcc-12-arm-linux-gnueabihf libubsan1-armhf-cross libc6-armhf-cross libc6-dev-armhf-cross qemu-user binutils-arm-linux-gnueabihf patch make 2>&1
	fi
	if [ -f /opt/targetcheck-tool-versions.txt ]; then
		printf 'pinned image record (/opt/targetcheck-tool-versions.txt):\n'
		sed 's/^/  /' /opt/targetcheck-tool-versions.txt
	fi
} >"$demo/environment.txt"

check_row() { # RUN ROW
	set +e
	env -i PATH="$PATH" LC_ALL=C TZ=UTC "$targetcheck" check \
		--manifest "$recipe/manifests/$2.json" --root "$1/root" --output "$1/reports/$2" \
		>"$1/cli/$2.stdout" 2>"$1/cli/$2.stderr"
	status=$?
	set -e
	printf '%s\n' "$status" >"$1/cli/$2.exit"
	printf 'demo:   %-34s CLI exit %s\n' "$2" "$status"
}

run_matrix() { # N
	run=$demo/run-$1
	started=$(date +%s)
	mkdir "$run" "$run/root" "$run/reports" "$run/cli" "$run/source" "$run/variants"
	# Every variant is extracted from a private copy that is verified again.
	cp "$tarball" "$run/source/wget-1.21.4.tar.gz"
	printf 'demo: run %s: creating fresh source variants\n' "$1"
	for entry in $rows; do
		row=${entry%%:*}
		pair=${entry#*:}
		mkdir "$run/root/$row"
		for variant in ${pair%%:*} ${pair#*:}; do
			sh "$recipe/make-variant.sh" "$variant" "$run/source/wget-1.21.4.tar.gz" "$run/root/$row/$variant" >"$run/variants/$row-$variant.log"
			cp "$run/root/$row/$variant/TARGETCHECK-VARIANT.txt" "$run/variants/$row-$variant.TARGETCHECK-VARIANT.txt"
		done
	done
	printf 'demo: run %s: running targetcheck for every committed manifest\n' "$1"
	for row in $preflight_rows; do
		check_row "$run" "$row"
	done
	for entry in $rows; do
		check_row "$run" "${entry%%:*}"
	done
	"$demo/bin/democheck" -recipe "$recipe" -run "$run" -normalized "$run/normalized.json" -summary "$run/summary.md"
	printf 'demo: run %s: verified in %ss\n' "$1" "$(($(date +%s) - started))"
}

run_matrix 1

# Negative path for the checker itself: rename the ARM candidate's named
# assertion in a copy of run 1 (same byte length, so only the diagnostic
# changes). The checker must reject it rather than accept any exit-1 failure.
tampered=$demo/tampered-run-1
mkdir "$tampered"
ln -s ../run-1/root "$tampered/root"
cp -R "$demo/run-1/reports" "$demo/run-1/cli" "$tampered/"
log=$tampered/reports/b-arm-large-offset-first-fix/logs/candidate-test-0.stdout
grep -q 'assertion range_accepted failed' "$log" || fail "unexpected ARM candidate log layout in $log"
sed 's/assertion range_accepted failed/assertion range_rejected failed/' "$log" >"$log.new"
mv "$log.new" "$log"
if "$demo/bin/democheck" -recipe "$recipe" -run "$tampered" -normalized "$tampered/normalized.json" -summary "$tampered/summary.md" >"$tampered/democheck.log" 2>&1; then
	fail "democheck accepted a tampered ARM diagnostic"
fi
grep -q 'b-arm-large-offset-first-fix candidate: test large-offset-first-byte-pos lacks named diagnostic' "$tampered/democheck.log" ||
	fail "democheck rejected the tampered run for another reason: $(cat "$tampered/democheck.log")"
printf 'demo: checker rejected a tampered ARM diagnostic, as required\n'

run_matrix 2
if ! cmp -s "$demo/run-1/normalized.json" "$demo/run-2/normalized.json"; then
	diff -u "$demo/run-1/normalized.json" "$demo/run-2/normalized.json" >&2 || true
	fail "the two fresh runs produced different normalized results"
fi
printf 'demo: run-1 and run-2 normalized coverage, outcomes, test output and digests are identical (sha256 %s)\n' \
	"$(sha256sum "$demo/run-1/normalized.json" | cut -d ' ' -f 1)"
sed -n '/^| Row /,/^$/p' "$demo/run-1/summary.md"
printf 'demo: PASS. Evidence: %s (environment.txt, run-N/summary.md, run-N/normalized.json, run-N/reports/)\n' "$demo"
