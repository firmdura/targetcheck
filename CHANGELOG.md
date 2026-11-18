# Changelog

All notable changes to TargetCheck are recorded here. Versions follow
[semantic versioning](https://semver.org/).
[Versions and compatibility](docs/usage.md#versions-and-compatibility)
describes what stays compatible within v1.

## v1.0.1

### Fixed

- JSON decoder syntax errors now include their one-based byte position in
  CLI diagnostics and library errors, including errors after a complete JSON
  value. This makes malformed manifests easier to fix. Validation rules,
  schema versions and exit codes are unchanged.

## v1.0.0

The first stable release. TargetCheck behaves as v0.1.0 did, apart from the
version it reports.

### Changed

- The `pkg/targetcheck` Go API, the CLI commands, flags and exit codes, and
  the manifest and report formats of `schema_version: 1` are now covered by
  the v1 compatibility promise.
- The documentation was rewritten for readability. It describes the same
  behavior and limits as before.
- The release archives are named `targetcheck-v1.0.0-linux-amd64.tar.gz` and
  `targetcheck-v1.0.0-linux-arm64.tar.gz`.

Upgrading from v0.1.0 needs no changes: manifests, reports and exit codes are
the same.

## v0.1.0

First release: a Go library and CLI for Linux amd64 and arm64 hosts that run
the same component regression suite against a baseline and a candidate,
natively or as ARM32 binaries under QEMU user mode, and write reviewable
evidence.

### Added

- Manifest schema version 1 ([schema/manifest-v1.json](schema/manifest-v1.json)):
  strict JSON with unknown- and duplicate-key rejection, a `native` or
  `qemu-arm` target with an explicit QEMU executable and sysroot, two
  variants in separate, non-nested directories with ordered build commands,
  declared inputs and artifacts, and up
  to 256 shared tests with exact exit codes and literal stdout/stderr markers.
  A nonzero baseline expectation requires a marker; candidate expectations
  require exit 0.
- Preflight of both variants before any command runs, including path,
  symlink, tool, QEMU, sysroot and ELF target checks.
- Bounded execution without an implicit shell: per-command timeouts of 1 to
  3600 seconds, 1 MiB stdout and stderr capture limits, process-group
  termination, and cancellation through the caller's context or a signal:
  SIGINT, SIGTERM and SIGHUP cancel (exit 130) unless the signal was ignored
  when TargetCheck started (e.g. `nohup`); Go keeps such an inherited ignore
  only for SIGINT and SIGHUP, so SIGTERM always cancels.
- SHA-256 identities of the manifest, declared inputs and tested artifacts
  before and after testing; artifact ELF class, machine and ABI; tool paths and
  the emulator's own `--version` line; environment override names, never
  values.
- Evaluation with explicit `matched`, `mismatch`, `not_run` and `error`
  checks and the precedence cancellation, infrastructure, mismatch, matched.
- Report schema version 1 ([schema/report-v1.json](schema/report-v1.json)):
  authoritative JSON and an escaped Markdown rendering.
- `targetcheck check` and `targetcheck version`: a new output directory with
  `report.json`, `report.md` and restricted-permission per-step logs; a
  one-line stderr diagnostic with the outcome, failure kind and message that
  names the failing check; and exit codes 0 (matched), 1 (mismatch), 2 (invalid
  input, infrastructure or report write failure) and 130 (cancelled).
- The Go package `github.com/firmdura/targetcheck/pkg/targetcheck` with
  `Load`, `Run`, `WriteJSON` and `WriteMarkdown` and the same semantics as the
  CLI.
- A synthetic native and ARM32 [quickstart](examples/quickstart/README.md).
- The [GNU Wget 1.21.4 CVE-2026-58470 example](examples/wget/README.md):
  upstream fix 43d3ba9, follow-up 01ff771 and a labelled local guard, tested
  through Wget's own unit-test program natively under UndefinedBehaviorSanitizer
  and on ARM32 under `qemu-arm`. GPL-3.0-or-later Wget-derived files are kept
  separate from the Apache-2.0 code.
- Make targets `verify`, `test-native`, `test-arm`, `prepare-demo`, `demo`,
  `dist` and `test-container`, a digest-pinned Linux development image, CI
  that runs the same targets, and reproducible Linux amd64 and arm64 release
  archives with SHA-256 checksums.
