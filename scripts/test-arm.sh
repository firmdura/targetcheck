#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# make test-arm: cross-build the ARM32 hard-float component in testdata/arm/fixture
# and execute it through the TargetCheck CLI under real qemu-arm -L SYSROOT.
# Covers matched and mismatched comparisons, preflight failures for a missing
# emulator or sysroot (exit 2), a missing dynamic loader, a native artifact
# and a static artifact. A missing tool is an actionable failure, never a skip.
# No network access is needed.
set -eu

sysroot=/usr/arm-linux-gnueabihf
missing=
for tool in ${GO:-go} gofmt qemu-arm arm-linux-gnueabihf-gcc gcc; do
	command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
for file in lib/ld-linux-armhf.so.3 lib/libc.so.6 lib/libc.a; do
	[ -f "$sysroot/$file" ] || missing="$missing $sysroot/$file"
done
if [ -n "$missing" ]; then
	printf 'test-arm: FAIL: missing%s.\n' "$missing" >&2
	printf 'test-arm: install qemu-user, gcc-arm-linux-gnueabihf and libc6-dev-armhf-cross, or use the pinned container (make container-build demo-container). Nothing was skipped.\n' >&2
	exit 2
fi
printf 'test-arm: %s\n' "$(qemu-arm --version | head -n 1)"
printf 'test-arm: %s\n' "$(arm-linux-gnueabihf-gcc --version | head -n 1)"

# testdata is outside ./..., so check its Go file here.
bad=$(gofmt -l testdata/arm)
[ -z "$bad" ] || { printf 'test-arm: run gofmt on: %s\n' "$bad" >&2; exit 1; }
${GO:-go} vet ./testdata/arm
${GO:-go} test -count=1 -v ./testdata/arm
