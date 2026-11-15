#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# Prepare the real Wget example: fetch the pinned public GNU Wget 1.21.4
# source into the ignored cache and verify its SHA-256. This is the only
# network step of the example; TargetCheck itself never downloads anything.
#
#   sh scripts/prepare-demo.sh                # fetch if absent, then verify
#   sh scripts/prepare-demo.sh --verify-only  # never fetch; verify the cache
#
# A cached tarball whose SHA-256 differs is refused with a nonzero exit; it is
# never extracted, replaced or used. Nothing is extracted here: the offline
# demo re-verifies a private copy before extracting it. Portable POSIX sh for
# Linux and macOS hosts: needs curl (for fetching) and sha256sum, shasum or
# openssl. WGET_CACHE_DIR overrides the cache directory (default .cache/wget).
set -eu

url=https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz
expected=81542f5cefb8faacc39bbbc6c82ded80e3e4a88505ae72ea51df27525bcde04c
cache=${WGET_CACHE_DIR:-.cache/wget}
tarball=$cache/wget-1.21.4.tar.gz

fail() {
	printf 'prepare-demo: %s\n' "$*" >&2
	exit 1
}

case ${1:-} in
'') fetch=yes ;;
--verify-only) fetch=no ;;
*) fail "usage: prepare-demo.sh [--verify-only]" ;;
esac
[ "$#" -le 1 ] || fail "usage: prepare-demo.sh [--verify-only]"

digest() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{ print $NF }'
	else
		fail "no SHA-256 tool found (install coreutils sha256sum, perl shasum or openssl)"
	fi
}

if [ -e "$tarball" ] || [ -L "$tarball" ]; then
	[ -f "$tarball" ] && [ ! -L "$tarball" ] || fail "$tarball is not an ordinary file; remove it and rerun make prepare-demo"
	actual=$(digest "$tarball")
	if [ "$actual" != "$expected" ]; then
		fail "refusing $tarball: SHA-256 $actual does not match the pinned $expected. It was not extracted or used. Remove it and rerun make prepare-demo."
	fi
	printf 'prepare-demo: verified %s (SHA-256 %s)\n' "$tarball" "$actual"
	exit 0
fi

[ "$fetch" = yes ] || fail "$tarball is missing; run make prepare-demo (network) first"
command -v curl >/dev/null 2>&1 || fail "curl is required to fetch $url"
mkdir -p "$cache"
partial=$(mktemp "$cache/.wget-1.21.4.tar.gz.XXXXXX")
trap 'rm -f "$partial"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
printf 'prepare-demo: fetching %s\n' "$url"
curl --fail --location --proto '=https' --proto-redir '=https' --silent --show-error --output "$partial" "$url" ||
	fail "download of $url failed"
actual=$(digest "$partial")
[ "$actual" = "$expected" ] || fail "downloaded file has SHA-256 $actual, expected $expected; it was discarded"
chmod 0644 "$partial"
mv "$partial" "$tarball"
trap - EXIT
printf 'prepare-demo: fetched and verified %s (SHA-256 %s)\n' "$tarball" "$actual"
