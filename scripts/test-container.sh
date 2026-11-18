#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# make test-container: run `make TARGET...` (default: verify test-native
# test-arm demo dist) in the pinned development image with --network=none,
# from a clean snapshot of the committed HEAD plus, when demo runs, the Wget
# tarball that make prepare-demo fetched and verified (WGET_CACHE_DIR, default
# .cache/wget: the file that is verified is the file that is streamed, as
# .cache/wget/wget-1.21.4.tar.gz inside the container). Works from Linux and
# macOS Docker hosts: the snapshot streams on stdin, so no bind mount or build
# context is used, and uncommitted edits, untracked files and local logs are
# not part of it.
#
# Evidence streams back into a new directory .cache/test-container/COMMIT.XXXXXX/
# even when a target fails: test-container.log (the complete make output),
# exit-status, tool-versions.txt (recorded in the image), wget-demo/ (reports,
# logs and summaries; not build trees) and dist/ (archives and SHA256SUMS).
# When dist runs, its archives must come back and their checksums are
# validated again on the host after extraction.
#
# Host prerequisites: git 2.30 or later, Docker with the image from
# make container-build, POSIX sh, tar and sha256sum or shasum.
set -eu

image=${DEV_IMAGE:-targetcheck-dev:go1.27.4}
version=${VERSION:-v1.0.1}
cache=${WGET_CACHE_DIR:-.cache/wget}
tarball=$cache/wget-1.21.4.tar.gz
targets=${*:-verify test-native test-arm demo dist}

fail() {
	printf 'test-container: FAIL: %s\n' "$*" >&2
	exit 1
}

[ -f Makefile ] && [ -f scripts/test-container.sh ] || fail "run from the repository root (make test-container)"
for tool in git docker tar; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is required on the host"
done
[ "$(git rev-parse --show-toplevel 2>/dev/null)" = "$(pwd -P)" ] || fail "run from the top of a git checkout"
commit=$(git rev-parse --verify 'HEAD^{commit}')
epoch=$(git log -1 --format=%ct HEAD)
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
	printf 'test-container: uncommitted changes are not tested; testing committed HEAD %s\n' "$commit" >&2
fi
docker image inspect "$image" >/dev/null 2>&1 ||
	fail "the pinned image $image is missing; run make container-build (network) first"

demo=no
dist=no
for target in $targets; do
	[ "$target" = demo ] && demo=yes
	[ "$target" = dist ] && dist=yes
done
if [ "$demo" = yes ]; then
	# Refuses a missing or corrupted cache before anything starts; never fetches.
	WGET_CACHE_DIR=$cache sh scripts/prepare-demo.sh --verify-only
fi

mkdir -p .cache/test-container
evidence=$(mktemp -d ".cache/test-container/$(printf '%.12s' "$commit").XXXXXX")
printf 'test-container: running make %s for %s in %s (--network=none)\n' "$targets" "$commit" "$image" >&2

# The container prints only the evidence tar on stdout; make output goes to
# stderr and into test-container.log.
inner='
set -u
tar -xf - -C /src || exit 2
cd /src || exit 2
out=/tmp/evidence
mkdir -p "$out"
cp /opt/targetcheck-tool-versions.txt "$out/tool-versions.txt" 2>/dev/null || echo "missing /opt/targetcheck-tool-versions.txt" >"$out/tool-versions.txt"
{ make $TARGETS; echo $? >"$out/exit-status"; } 2>&1 | tee "$out/test-container.log" >&2
status=$(cat "$out/exit-status")
if [ -d .cache/wget-demo ]; then
	tar -cf - -C .cache --exclude="*/root" --exclude="*/source" --exclude="*/bin" --exclude="*.tar.gz" wget-demo | tar -xf - -C "$out"
fi
if [ -d dist ]; then cp -R dist "$out/dist"; fi
tar -cf - -C "$out" .
exit "$status"
'
set +e
if [ "$demo" = yes ]; then
	git archive --format=tar --prefix=.cache/wget/ --add-file="$tarball" --prefix= HEAD
else
	git archive --format=tar HEAD
fi | docker run --rm --pull=never --network=none --init -i \
	-e TARGETCHECK_COMMIT="$commit" -e SOURCE_DATE_EPOCH="$epoch" -e VERSION="$version" -e TARGETS="$targets" \
	"$image" sh -c "$inner" >"$evidence/evidence.tar"
status=$?
set -e

if [ -s "$evidence/evidence.tar" ] && tar -xf "$evidence/evidence.tar" -C "$evidence"; then
	rm -f "$evidence/evidence.tar"
else
	printf 'test-container: the container returned no evidence (docker exit %s)\n' "$status" >&2
	[ "$status" -ne 0 ] || status=1
fi
if [ -f "$evidence/exit-status" ] && [ "$(cat "$evidence/exit-status")" != "$status" ]; then
	printf 'test-container: make exited %s but the container exited %s\n' "$(cat "$evidence/exit-status")" "$status" >&2
	[ "$status" -ne 0 ] || status=1
fi

# Validate the returned release artifacts again, outside the container. A dist
# run that reports success must bring back both archives and SHA256SUMS.
problem=
if [ "$status" -eq 0 ] && [ "$dist" = yes ]; then
	for file in SHA256SUMS targetcheck-$version-linux-amd64.tar.gz targetcheck-$version-linux-arm64.tar.gz; do
		[ -f "$evidence/dist/$file" ] || problem="${problem:+$problem; }make dist succeeded but dist/$file did not come back"
	done
	if [ -z "$problem" ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			check='sha256sum -c'
		elif command -v shasum >/dev/null 2>&1; then
			check='shasum -a 256 -c'
		else
			check=
			problem="no sha256sum or shasum on the host to validate $evidence/dist"
		fi
	fi
	if [ -z "$problem" ]; then
		if (cd "$evidence/dist" && $check SHA256SUMS) >&2; then
			printf 'test-container: dist checksums validated on the host after extraction\n' >&2
		else
			problem="the returned dist archives do not match SHA256SUMS"
		fi
	fi
	[ -z "$problem" ] || status=1
fi

printf 'test-container: evidence in %s\n' "$evidence" >&2
if [ "$status" -eq 0 ]; then
	printf 'test-container: PASS (make %s)\n' "$targets" >&2
elif [ -n "$problem" ]; then
	printf 'test-container: FAIL: %s\n' "$problem" >&2
elif [ -f "$evidence/test-container.log" ]; then
	printf 'test-container: FAIL: exit %s; see %s/test-container.log\n' "$status" "$evidence" >&2
else
	printf 'test-container: FAIL: exit %s before any make output came back; see the messages above\n' "$status" >&2
fi
exit "$status"
