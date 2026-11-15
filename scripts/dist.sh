#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# make dist: build the versioned Linux amd64 and arm64 release archives and
# their SHA-256 checksums in dist/, then verify them. Offline; needs the
# pinned Linux environment: Go 1.27.4, GNU tar, gzip, sha256sum and file; cc,
# arm-linux-gnueabihf-gcc, qemu-arm and the /usr/arm-linux-gnueabihf sysroot for
# the bundled quickstarts; and, for the architecture that is not the host's,
# qemu-x86_64 or qemu-aarch64. All of them are checked before anything is built.
#
# Only committed content is used. In a git checkout the archives are built
# from `git archive HEAD`, so uncommitted edits and untracked or ignored files,
# such as local logs and caches, are never read. In a snapshot without .git, such as
# the one make test-container streams into the container, TARGETCHECK_COMMIT
# and SOURCE_DATE_EPOCH must name the commit the snapshot was archived from.
#
# Each archive targetcheck-VERSION-linux-ARCH.tar.gz holds one directory with
# the static CLI, LICENSE (Apache-2.0), the Go toolchain's LICENSE and PATENTS
# for the Go runtime and standard library linked into the CLI, README.md,
# CHANGELOG.md, docs/, schema/, the Apache-2.0 quickstart example and a
# SHA256SUMS file covering every other file. Relative links in the bundled
# Markdown whose targets are not in the archive are rewritten to the same
# repository path at the release tag, https://github.com/firmdura/targetcheck/
# blob/VERSION/PATH (tree/ for directories); links inside the archive stay
# relative. The GPL-covered Wget example is not part of the CLI and is not
# packaged. VERSION and the commit are embedded
# with -ldflags; archives use sorted names, the commit time as mtime, numeric
# owner 0/0, fixed modes and gzip -n, so they are reproducible.
#
# Verification: everything is built a second time from a copy in another
# directory, with a new, empty Go build cache, and must be byte-identical. Each
# archive is then extracted into a new directory, its file list, modes, owners,
# inner checksums, relative links and binary build metadata are checked, and its
# CLI is smoke-tested on its architecture,
# natively or under QEMU user-mode emulation, including the bundled quickstart
# instructions in examples/quickstart/README.md. Nothing is skipped: a missing
# tool fails the target.
set -eu

module=github.com/firmdura/targetcheck
archs='amd64 arm64'
repo=$(pwd -P)
go=${GO:-go}
version=${VERSION:-}
dist=$repo/dist

fail() {
	printf 'dist: FAIL: %s\n' "$*" >&2
	exit 1
}
note() {
	printf 'dist: %s\n' "$*"
}

[ "$(uname -s)" = Linux ] || fail "Linux is required; on macOS run make test-container"
[ -f "$repo/go.mod" ] && [ -f "$repo/scripts/dist.sh" ] || fail "run from the repository root (make dist)"
printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$' ||
	fail "VERSION must be a semantic version tag such as v1.0.0, not '$version'"
missing=
for tool in "$go" git tar gzip sha256sum file awk sed sort find cmp cc arm-linux-gnueabihf-gcc qemu-arm; do
	command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
# The smoke tests build and run the bundled native and ARM32 quickstarts.
for file in /usr/arm-linux-gnueabihf/lib/ld-linux-armhf.so.3 /usr/arm-linux-gnueabihf/lib/libc.so.6; do
	[ -f "$file" ] || missing="$missing $file"
done
[ -z "$missing" ] || fail "missing required tools or ARM sysroot files:$missing. The smoke tests build and run the bundled native and ARM32 quickstarts; install the Debian packages gcc, gcc-arm-linux-gnueabihf, libc6-dev-armhf-cross and qemu-user, or use the pinned container (make test-container). Nothing was built."
tar --version 2>/dev/null | head -n 1 | grep -q 'GNU tar' || fail "GNU tar is required for reproducible archives"
case $(uname -m) in
x86_64 | amd64) host=amd64 ;;
aarch64 | arm64) host=arm64 ;;
*) fail "unsupported host architecture $(uname -m); TargetCheck supports Linux amd64 and arm64" ;;
esac
for arch in $archs; do
	[ "$arch" = "$host" ] && continue
	case $arch in amd64) emulator=qemu-x86_64 ;; *) emulator=qemu-aarch64 ;; esac
	command -v "$emulator" >/dev/null 2>&1 ||
		fail "$emulator is required to smoke-test the linux/$arch CLI on this linux/$host host (Debian package qemu-user). Nothing was skipped."
done
goversion=$("$go" env GOVERSION)
goroot=$("$go" env GOROOT)
for file in LICENSE PATENTS; do
	[ -f "$goroot/$file" ] || fail "the Go toolchain's $file is missing from $goroot"
done

work=$(mktemp -d "${TMPDIR:-/tmp}/targetcheck-dist.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The committed source and its identity.
mkdir "$work/src"
if git -C "$repo" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	[ "$(git -C "$repo" rev-parse --show-toplevel)" = "$repo" ] || fail "run from the top of the git checkout"
	commit=$(git -C "$repo" rev-parse --verify 'HEAD^{commit}')
	epoch=$(git -C "$repo" log -1 --format=%ct HEAD)
	[ -z "${TARGETCHECK_COMMIT:-}" ] || [ "$TARGETCHECK_COMMIT" = "$commit" ] ||
		fail "TARGETCHECK_COMMIT=$TARGETCHECK_COMMIT is not HEAD ($commit)"
	[ -z "${SOURCE_DATE_EPOCH:-}" ] || [ "$SOURCE_DATE_EPOCH" = "$epoch" ] ||
		fail "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH is not the HEAD commit time ($epoch)"
	if [ -n "$(git -C "$repo" status --porcelain --untracked-files=no)" ]; then
		note "the checkout has uncommitted changes; they are not included (building committed HEAD $commit)"
	fi
	git -C "$repo" archive --format=tar HEAD | tar -xf - -C "$work/src"
	tree=$work/src
	source_note="git archive of HEAD"
elif [ -e "$repo/.git" ]; then
	reason=$(git -C "$repo" rev-parse --is-inside-work-tree 2>&1 | head -n 1 || true)
	fail "git cannot read this checkout ($reason), so the committed content and commit are unknown. Run make test-container, or, in a throwaway container you own, run git config --global --add safe.directory '$repo' first."
else
	commit=${TARGETCHECK_COMMIT:-}
	epoch=${SOURCE_DATE_EPOCH:-}
	printf '%s\n' "$commit" | grep -Eq '^[0-9a-f]{40}$' ||
		fail "no git checkout: set TARGETCHECK_COMMIT to the 40-digit commit this snapshot was archived from (make test-container does)"
	printf '%s\n' "$epoch" | grep -Eq '^[0-9]+$' ||
		fail "no git checkout: set SOURCE_DATE_EPOCH to that commit's time (git log -1 --format=%ct)"
	# Copy only the paths a release uses, so files created after extraction
	# (for example by earlier make targets) cannot enter the archives.
	for path in go.mod cmd internal pkg LICENSE README.md CHANGELOG.md docs schema examples/quickstart; do
		[ -e "$repo/$path" ] || fail "snapshot is missing $path"
		mkdir -p "$work/src/$(dirname "$path")"
		cp -R "$repo/$path" "$work/src/$path"
	done
	tree=$repo
	source_note="snapshot of commit $commit (TARGETCHECK_COMMIT)"
fi
src=$work/src
awk -v heading="## $version" '$0 == heading || index($0, heading " ") == 1 { found = 1 } END { exit !found }' "$src/CHANGELOG.md" ||
	fail "CHANGELOG.md has no '## $version' entry; archives are only built for a documented version"
note "building $version from the $source_note, commit time $epoch, with $goversion"

ldflags="-s -w -X $module/internal/buildinfo.Version=$version -X $module/internal/buildinfo.Commit=$commit"

# Every directory (D) and file (F) of the committed tree, for the link rewriting
# below; caches, earlier build output and VCS metadata are not part of it.
for kind in d f; do
	(cd "$tree" && find . -mindepth 1 \( -path ./.git -o -path ./.cache -o -path ./dist -o -path ./bin \) -prune -o -type "$kind" -print) |
		sed "s|^\./|$(printf '%s' "$kind" | tr df DF) |"
done | LC_ALL=C sort >"$work/repo-paths"

# links.awk reads a path list ("A PATH" for archive entries, "F PATH" and
# "D PATH" for repository files and directories), then one Markdown file whose
# directory inside the archive is base. Outside fenced code blocks and inline
# code spans, a relative link target that is not in the archive becomes the
# same repository path at the release tag (mode=rewrite) or is reported
# (mode=check). Absolute URLs and in-page anchors are left alone.
cat >"$work/links.awk" <<'AWK'
function normalize(p,   n, parts, i, k, out, s) {
	n = split(p, parts, "/")
	k = 0
	for (i = 1; i <= n; i++) {
		if (parts[i] == "" || parts[i] == ".")
			continue
		if (parts[i] == "..") {
			if (k == 0)
				return ""
			k--
			continue
		}
		out[++k] = parts[i]
	}
	s = out[1]
	for (i = 2; i <= k; i++)
		s = s "/" out[i]
	return k == 0 ? "" : s
}
function resolve(target,   hash, frag, rel, path) {
	if (target ~ /^[A-Za-z][A-Za-z0-9+.-]*:/ || target ~ /^#/ || target ~ /^\//)
		return target
	frag = ""
	rel = target
	hash = index(target, "#")
	if (hash > 0) {
		frag = substr(target, hash)
		rel = substr(target, 1, hash - 1)
	}
	path = normalize(base == "" ? rel : base "/" rel)
	if (path != "" && path in archive)
		return target
	if (mode == "rewrite" && path in file)
		return "https://github.com/firmdura/targetcheck/blob/" version "/" path frag
	if (mode == "rewrite" && path in dir)
		return "https://github.com/firmdura/targetcheck/tree/" version "/" path frag
	printf "%s: relative link %s has no target%s\n", FILENAME, target, mode == "check" ? " in the archive" : " in the repository" >"/dev/stderr"
	bad = 1
	return target
}
function links(text,   out) {
	out = ""
	while (match(text, /\]\([^)]*\)/)) {
		out = out substr(text, 1, RSTART + 1) resolve(substr(text, RSTART + 2, RLENGTH - 3)) ")"
		text = substr(text, RSTART + RLENGTH)
	}
	return out text
}
FNR == NR {
	kind = substr($0, 1, 2)
	path = substr($0, 3)
	if (kind == "A ")
		archive[path] = 1
	else if (kind == "F ")
		file[path] = 1
	else if (kind == "D ")
		dir[path] = 1
	next
}
/^[ \t]*```/ { fence = !fence; code = 0; print; next }
fence { print; next }
/^[ \t]*$/ { code = 0 }
{
	# Inline code spans may continue onto the next line of a paragraph.
	n = split($0, part, "`")
	line = ""
	for (i = 1; i <= n; i++) {
		line = line (code ? part[i] : links(part[i]))
		if (i < n) {
			line = line "`"
			code = !code
		}
	}
	print line
}
END { exit bad }
AWK

# links MODE DIR: rewrite or check every Markdown file below archive directory DIR.
links() {
	(cd "$2" && find . -mindepth 1 | sed 's|^\./|A |') >"$work/link-paths"
	[ "$1" = check ] || cat "$work/repo-paths" >>"$work/link-paths"
	for doc in $(cd "$2" && find . -type f -name '*.md' | sed 's|^\./||' | LC_ALL=C sort); do
		base=$(dirname "$doc")
		[ "$base" != . ] || base=
		awk -v base="$base" -v version="$version" -v mode="$1" -f "$work/links.awk" "$work/link-paths" "$2/$doc" >"$work/doc.tmp" ||
			fail "$doc: a relative link has no target (see above)"
		if [ "$1" = rewrite ]; then mv "$work/doc.tmp" "$2/$doc"; fi
	done
}

# stage SRC OUT ARCH GOCACHE LABEL: one archive directory, with fixed modes.
# go build -x records every compile step in build-LABEL-ARCH.log.
stage() {
	(cd "$1" && env CGO_ENABLED=0 GOOS=linux GOARCH="$3" GOFLAGS=-buildvcs=false GOEXPERIMENT= GOCACHE="$4" \
		"$go" build -x -trimpath -ldflags "$ldflags" -o "$2/targetcheck" ./cmd/targetcheck) 2>"$work/build-$5-$3.log" ||
		{ cat "$work/build-$5-$3.log" >&2; fail "go build for linux/$3 failed"; }
	mkdir -p "$2/docs" "$2/schema" "$2/examples" "$2/third_party/go"
	cp "$1/LICENSE" "$1/README.md" "$1/CHANGELOG.md" "$2/"
	cp "$1"/docs/*.md "$2/docs/"
	cp "$1"/schema/*.json "$2/schema/"
	cp -R "$1/examples/quickstart" "$2/examples/quickstart"
	cp "$goroot/LICENSE" "$goroot/PATENTS" "$2/third_party/go/"
	links rewrite "$2"
	find "$2" -type d -exec chmod 0755 {} +
	find "$2" -type f -exec chmod 0644 {} +
	chmod 0755 "$2/targetcheck"
	(cd "$2" && find . -type f ! -path ./SHA256SUMS | LC_ALL=C sort | sed 's|^\./||' | while IFS= read -r file; do sha256sum "$file"; done) >"$work/SHA256SUMS.tmp"
	mv "$work/SHA256SUMS.tmp" "$2/SHA256SUMS"
	chmod 0644 "$2/SHA256SUMS"
}

# package SRC OUTDIR GOCACHE LABEL: both archives and SHA256SUMS into OUTDIR.
package() {
	mkdir -p "$2"
	for arch in $archs; do
		name=targetcheck-$version-linux-$arch
		rm -rf "$work/stage"
		mkdir "$work/stage"
		stage "$1" "$work/stage/$name" "$arch" "$3" "$4"
		(cd "$work/stage" && LC_ALL=C tar --sort=name --format=gnu --owner=0 --group=0 --numeric-owner \
			--mtime="@$epoch" --no-acls --no-xattrs --no-selinux -cf - "$name") | gzip -9 -n >"$2/$name.tar.gz"
	done
	(cd "$2" && for arch in $archs; do sha256sum "targetcheck-$version-linux-$arch.tar.gz"; done) >"$2/SHA256SUMS"
}

package "$src" "$work/out" "$("$go" env GOCACHE)" first
# Reproducibility: another copy of the source in another directory, compiled
# from scratch with a new, empty build cache.
cp -R "$src" "$work/src-again"
mkdir "$work/gocache-empty"
package "$work/src-again" "$work/out-again" "$work/gocache-empty" second
for arch in $archs; do
	first=$(grep -c '/compile -o ' "$work/build-first-$arch.log" || true)
	second=$(grep -c '/compile -o ' "$work/build-second-$arch.log" || true)
	[ "$second" -gt 0 ] && [ "$second" -ge "$first" ] ||
		fail "linux/$arch: the second build ran $second compile steps (first build: $first), so it did not start from an empty cache"
	note "linux/$arch: the first build ran $first compile steps, the second, with an empty GOCACHE, $second"
done
for file in SHA256SUMS $(for arch in $archs; do printf 'targetcheck-%s-linux-%s.tar.gz ' "$version" "$arch"; done); do
	cmp -s "$work/out/$file" "$work/out-again/$file" || fail "$file differs between two builds of the same commit; the build is not reproducible"
done
note "two builds from different directories, the second with an empty Go build cache, produced byte-identical archives"

(cd "$work/out" && sha256sum --strict -c SHA256SUMS >/dev/null) || fail "archive checksums do not verify"

# verify ARCH: extract one archive into a new directory and check it.
verify() {
	arch=$1
	name=targetcheck-$version-linux-$arch
	archive=$work/out/$name.tar.gz
	dir=$work/verify-$arch
	mkdir "$dir"
	tar -xzf "$archive" -C "$dir"
	[ "$(ls -A "$dir")" = "$name" ] || fail "$name.tar.gz must contain exactly the directory $name"
	top=$dir/$name
	(cd "$top" && sha256sum --strict -c SHA256SUMS >/dev/null) || fail "$name: extracted files do not match SHA256SUMS"
	listed=$(sed 's/^[0-9a-f]*  //' "$top/SHA256SUMS" | LC_ALL=C sort)
	present=$(cd "$top" && find . -type f ! -path ./SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort)
	[ "$listed" = "$present" ] || fail "$name: SHA256SUMS does not list exactly the extracted files"
	for file in targetcheck LICENSE README.md CHANGELOG.md docs/usage.md docs/library.md docs/limitations.md \
		docs/troubleshooting.md schema/manifest-v1.json schema/report-v1.json examples/quickstart/README.md \
		examples/quickstart/native.json examples/quickstart/arm.json third_party/go/LICENSE third_party/go/PATENTS; do
		[ -f "$top/$file" ] || fail "$name: $file is missing"
	done
	cmp -s "$top/LICENSE" "$src/LICENSE" || fail "$name: LICENSE differs from the repository LICENSE"
	grep -q 'Apache License' "$top/LICENSE" || fail "$name: LICENSE is not the Apache License"
	cmp -s "$top/third_party/go/LICENSE" "$goroot/LICENSE" || fail "$name: Go LICENSE differs from the toolchain's"
	# Only the release content may be present: no Wget material, caches or logs.
	unexpected=$(printf '%s\n' "$present" | grep -vE '^(targetcheck|LICENSE|README\.md|CHANGELOG\.md|docs/[a-z0-9-]+\.md|schema/[a-z0-9-]+\.json|examples/quickstart/[A-Za-z0-9/._-]+|third_party/go/(LICENSE|PATENTS))$' || true)
	[ -z "$unexpected" ] || fail "$name: unexpected files: $unexpected"
	leaked=$(cd "$top" && { grep -rlF "$work" . || true; grep -rlE '/Users/|/private/(tmp|var)/' . || true; })
	[ -z "$leaked" ] || fail "$name: absolute build or host paths in: $leaked"
	# No relative link may dangle; repository-only targets point to the tag.
	links check "$top"
	tagged=$(cat $(find "$top" -type f -name '*.md') | grep -oE "https://github.com/firmdura/targetcheck/(blob|tree)/$version/" | wc -l | tr -d ' ')
	note "$name: every relative link resolves inside the archive; $tagged links point to repository files at $version"
	# Owners, modes and times as stored in the archive.
	stamp=$(TZ=UTC date -d "@$epoch" '+%Y-%m-%d %H:%M:%S')
	bad=$(TZ=UTC tar --full-time -tvzf "$archive" | awk -v stamp="$stamp" -v top="$name/targetcheck" '
		$2 != "0/0" { print "owner " $2 ": " $NF; next }
		($4 " " $5) != stamp { print "mtime " $4 " " $5 ": " $NF; next }
		$1 == "-rwxr-xr-x" && $NF == top { next }
		$1 !~ /^(drwxr-xr-x|-rw-r--r--)$/ { print "mode " $1 ": " $NF }')
	[ -z "$bad" ] || fail "$name: unexpected archive metadata: $bad"
	# The binary: static, the right machine, the embedded version and commit.
	description=$(file -b "$top/targetcheck")
	case $arch in amd64) machine='x86-64' ;; *) machine='ARM aarch64' ;; esac
	case $description in
	*"ELF 64-bit LSB executable, $machine"*"statically linked"*) ;;
	*) fail "$name: targetcheck is not a static linux/$arch executable: $description" ;;
	esac
	metadata=$("$go" version -m "$top/targetcheck")
	tab=$(printf '\t')
	# With -trimpath, Go does not record -ldflags; the smoke test checks the
	# embedded version and commit through targetcheck version instead.
	for line in "$top/targetcheck: $goversion" "${tab}path${tab}$module/cmd/targetcheck" "${tab}build${tab}CGO_ENABLED=0" \
		"${tab}build${tab}GOOS=linux" "${tab}build${tab}GOARCH=$arch" "${tab}build${tab}-trimpath=true"; do
		printf '%s\n' "$metadata" | grep -Fxq "$line" || fail "$name: build metadata lacks '$line'"
	done
	if printf '%s\n' "$metadata" | grep -q "${tab}build${tab}vcs"; then fail "$name: unexpected VCS stamp in build metadata"; fi
	printf '%s\n' "$metadata" | sed "s|^$top/||; s|^|dist:   |"
	smoke "$arch" "$top"
	printf 'dist: %s.tar.gz verified: %s files, static linux/%s, "%s" (%s)\n' "$name" "$(printf '%s\n' "$listed" | wc -l | tr -d ' ')" "$arch" "$got" "$how"
}

# quickstart README NAME: the shell block after <!-- quickstart:NAME -->.
quickstart() {
	awk -v marker="<!-- quickstart:$2 -->" '
		$0 == marker { found = 1; next }
		found && !inside && $0 == "```sh" { inside = 1; next }
		inside && $0 == "```" { done = 1; exit }
		inside { print }
		END { exit !done }' "$1"
}

# smoke ARCH DIR: CLI smoke tests and bundled quickstart instructions.
smoke() {
	arch=$1
	top=$2
	bin=$work/smoke-$arch/bin
	tmp=$work/smoke-$arch/tmp
	mkdir -p "$bin" "$tmp"
	if [ "$arch" = "$host" ]; then
		printf '#!/bin/sh\nexec "%s/targetcheck" "$@"\n' "$top" >"$bin/targetcheck"
		how=native
	else
		case $arch in amd64) emulator=qemu-x86_64 ;; *) emulator=qemu-aarch64 ;; esac
		printf '#!/bin/sh\nexec %s "%s/targetcheck" "$@"\n' "$(command -v "$emulator")" "$top" >"$bin/targetcheck"
		how="under $emulator user-mode emulation"
	fi
	chmod 0755 "$bin/targetcheck"
	run_cli() { PATH="$bin:$PATH" TMPDIR="$tmp" targetcheck "$@"; }
	got=$(run_cli version) || fail "linux/$arch: targetcheck version failed ($how)"
	want="targetcheck $version commit=$commit go=$goversion"
	[ "$got" = "$want" ] || fail "linux/$arch: version printed '$got', expected '$want'"
	run_cli help >"$tmp/help.out" || fail "linux/$arch: targetcheck help did not exit 0"
	[ "$(head -n 1 "$tmp/help.out")" = "Usage:" ] || fail "linux/$arch: unexpected help output"
	set +e
	run_cli >/dev/null 2>&1
	usage=$?
	run_cli check --manifest "$top/SHA256SUMS" --root "$top" --output "$tmp/invalid" >"$tmp/invalid.out" 2>&1
	invalid=$?
	set -e
	[ "$usage" -eq 2 ] || fail "linux/$arch: a missing command exited $usage, expected 2"
	[ "$invalid" -eq 2 ] && grep -q '"outcome": "error"' "$tmp/invalid/report.json" && grep -q '"kind": "invalid_input"' "$tmp/invalid/report.json" ||
		fail "linux/$arch: an invalid manifest did not exit 2 with an invalid_input report: $(cat "$tmp/invalid.out")"
	# The bundled quickstart, as written. A foreign-architecture CLI cannot run
	# the native quickstart, whose artifacts are built for the host, so it runs
	# the ARM32 quickstart only.
	if [ "$arch" = "$host" ]; then blocks='native regression arm'; else blocks='arm'; fi
	for block in $blocks; do
		quickstart "$top/examples/quickstart/README.md" "$block" >"$tmp/$block.sh" ||
			fail "examples/quickstart/README.md has no '$block' instructions"
		set +e
		if [ "$block" = regression ]; then
			(cd "$top" && PATH="$bin:$PATH" TMPDIR="$tmp" sh -u "$tmp/$block.sh") >"$tmp/$block.log" 2>&1
		else
			(cd "$top" && PATH="$bin:$PATH" TMPDIR="$tmp" sh -eu "$tmp/$block.sh") >"$tmp/$block.log" 2>&1
		fi
		status=$?
		set -e
		case $block in
		native) grep -q '^targetcheck: matched$' "$tmp/$block.log" && grep -Fq 'FAIL reject-out-of-range: "70000" was accepted as 4464' "$tmp/$block.log" ;;
		regression) grep -q '^targetcheck: mismatch$' "$tmp/$block.log" && grep -q '^exit 1$' "$tmp/$block.log" ;;
		arm) grep -q '^targetcheck: matched$' "$tmp/$block.log" && grep -q '"class": "ELF32"' "$tmp/$block.log" ;;
		esac || status=1
		[ "$status" -eq 0 ] || fail "linux/$arch: quickstart '$block' ($how) did not give the documented result:
$(cat "$tmp/$block.log")"
		printf 'dist: linux/%s quickstart %-10s ok (%s)\n' "$arch" "$block" "$how"
	done
}

for arch in $archs; do
	verify "$arch"
done

# Publish into a fresh dist/.
rm -rf "$dist"
mkdir "$dist"
cp "$work/out"/* "$dist/"
(cd "$dist" && sha256sum --strict -c SHA256SUMS >/dev/null) || fail "copied archives do not verify"
note "PASS: $dist"
(cd "$dist" && ls -l && cat SHA256SUMS)
