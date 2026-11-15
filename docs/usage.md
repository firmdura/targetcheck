# Using TargetCheck

This guide shows you how to install TargetCheck, run a comparison and write a
manifest for your own component. The second half is reference material: the
manifest fields, the output directory, exit codes, reports and the version
policy.

Other guides cover the rest:

- [library.md](library.md): the Go API
- [troubleshooting.md](troubleshooting.md): what to do when a run fails
- [limitations.md](limitations.md): what a result does and does not show, and
  the trust model

## Key terms

| Term | Meaning |
| --- | --- |
| baseline | the build you compare against, usually the code before the fix |
| candidate | the build you want to check, usually the code with the fix |
| variant | either of the two builds. The manifest has one entry per variant |
| manifest | the JSON file that describes both variants and the tests they share |
| artifact | an executable that a variant's build produces and the tests run |
| expectation | what a test must do in one variant: exit with an exact code and, optionally, print a marker |
| marker | literal text that the test's stdout or stderr must contain |
| expected failure | a baseline expectation with a nonzero exit code, which states the known defect exactly. It is also called an expected negative test |
| preflight | the checks TargetCheck makes on both variants before it runs any command |
| sysroot | a directory with the ARM32 target's dynamic loader and libraries, which QEMU gets as `-L SYSROOT`. QEMU looks up the program's loader and libraries there |

## Supported hosts and targets

TargetCheck runs on Linux. Builds always run on the host. Tests run either
directly on the host or under QEMU user mode, which runs a single ARM32 Linux
program on your host's kernel without booting a system.

| | Supported |
| --- | --- |
| Host | Linux amd64 (x86-64) and Linux arm64 (AArch64) |
| `native` target | ELF64 little-endian Linux executables for the host architecture |
| `qemu-arm` target | little-endian ARM32 EABI5 hard-float ELF executables, run as `qemu-arm -L SYSROOT ARTIFACT ARGS...` |
| Builds | always run on the host, for either target |

EABI5 hard-float is the 32-bit ARM Linux ABI that Debian calls armhf. It is
what `arm-linux-gnueabihf-gcc` produces. Other ARM variants, such as
soft-float or big-endian ARM, are not supported.

On any other operating system or architecture, `targetcheck check` and
`targetcheck.Run` stop in preflight with `supported hosts are Linux amd64 and
arm64` (exit 2). They start no command. On macOS, use the
[pinned Linux container](#the-pinned-development-container).

A dynamically linked ARM32 artifact needs its ELF interpreter inside the
sysroot (for glibc, `lib/ld-linux-armhf.so.3`), and its shared libraries too.
A statically linked artifact can use an otherwise empty sysroot directory.

## Install

You can install the CLI from a release archive, with `go install`, or from a
source checkout. To use the Go library, see [library.md](library.md).

### Release archives

The [v1.0.0 release](https://github.com/firmdura/targetcheck/releases/tag/v1.0.0)
has one archive per architecture and a `SHA256SUMS` file:

| File | Content |
| --- | --- |
| `targetcheck-v1.0.0-linux-amd64.tar.gz` | static CLI for Linux x86-64 |
| `targetcheck-v1.0.0-linux-arm64.tar.gz` | static CLI for Linux AArch64 |
| `SHA256SUMS` | SHA-256 of both archives |

These commands download one archive, check it against `SHA256SUMS`, unpack
it, and then check every unpacked file against the archive's own
`SHA256SUMS`:

```sh
version=v1.0.0
arch=amd64 # or arm64
curl -fLO "https://github.com/firmdura/targetcheck/releases/download/$version/targetcheck-$version-linux-$arch.tar.gz"
curl -fLO "https://github.com/firmdura/targetcheck/releases/download/$version/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf "targetcheck-$version-linux-$arch.tar.gz"
cd "targetcheck-$version-linux-$arch"
sha256sum -c SHA256SUMS
./targetcheck version
```

Every archive holds a single directory, `targetcheck-v1.0.0-linux-ARCH/`,
with these files:

| Path | Content |
| --- | --- |
| `targetcheck` | the CLI, statically linked (`CGO_ENABLED=0`), built with Go 1.27.4 |
| `LICENSE` | Apache License 2.0, for TargetCheck |
| `third_party/go/LICENSE`, `third_party/go/PATENTS` | the Go license and patent grant for the Go runtime and standard library in the binary |
| `README.md`, `CHANGELOG.md`, `docs/` | this documentation |
| `schema/` | the manifest and report JSON schemas |
| `examples/quickstart/` | the synthetic quickstart component and manifests |
| `SHA256SUMS` | SHA-256 of every other file in the directory |

Put the directory on your `PATH` with `export PATH="$PWD:$PATH"`, or copy
`targetcheck` to a directory that is on it.

`targetcheck version` prints the release version, the commit the archive was
built from and the Go version, for example
`targetcheck v1.0.0 commit=<40-digit commit> go=go1.27.4`.

Two things to know about the archives:

- The bundled documents link to some repository files that the archive does
  not contain, such as the Wget example. Those links point to the same files
  at the `v1.0.0` tag on GitHub.
- The archives are reproducible. `make dist` on the same commit in the pinned
  environment produces byte-identical files. `make dist` also checks this
  itself: it builds everything a second time, from another directory with an
  empty Go build cache, and requires identical bytes.

### Go module

With Go 1.27.4 or later, this command installs `targetcheck` into
`$(go env GOPATH)/bin`:

```sh
go install github.com/firmdura/targetcheck/cmd/targetcheck@v1.0.0
```

A module download has no version control metadata, so this build reports
`targetcheck v1.0.0 commit=unknown go=GOVERSION`. For the library, run
`go get github.com/firmdura/targetcheck@v1.0.0` and see
[library.md](library.md).

### Source checkout

```sh
git clone https://github.com/firmdura/targetcheck.git
cd targetcheck
git checkout v1.0.0
go build -o bin/targetcheck ./cmd/targetcheck
```

Go stamps such a build with version control information. In a clean checkout
of the tag, `bin/targetcheck version` reports `v1.0.0` and the commit. A build
from another commit reports a Go pseudo-version, with `+dirty` if there are
uncommitted changes. A build with `-buildvcs=false`, as the Make targets use,
reports `dev commit=unknown`.

## Prerequisites

TargetCheck itself has no runtime dependencies and installs nothing. The build
and test commands in your manifest need their own tools:

| For | You provide |
| --- | --- |
| native tests | the compilers and build tools that your build commands name, for example `cc` or `make` |
| ARM32 tests | `qemu-arm` from QEMU user mode, an ARM32 hard-float cross compiler and a sysroot. In Debian, which the pinned image uses, the packages `qemu-user`, `gcc-arm-linux-gnueabihf` and `libc6-dev-armhf-cross` provide `qemu-arm`, `arm-linux-gnueabihf-gcc` and `/usr/arm-linux-gnueabihf` |

The v1.0.0 acceptance ran in the pinned image with Go 1.27.4, GCC 12.2.0,
`arm-linux-gnueabihf-gcc` 12.2.0 and QEMU 7.2.22. Other versions are not part
of that evidence.

### The pinned development container

The repository's acceptance environment is a Linux image defined by
[dev/Containerfile](../dev/Containerfile). It starts from a digest-pinned
official Go 1.27.4 image and installs Debian packages from a dated snapshot.
[dev/tool-versions.txt](../dev/tool-versions.txt) lists the packages and their
observed versions. The image runs on Linux and macOS Docker hosts. The v1.0.0
acceptance used Colima on macOS arm64.

The host needs:

| Host needs | For |
| --- | --- |
| Docker | every container target |
| git 2.30 or later, POSIX `sh`, `tar` | streaming the committed snapshot (`git archive --add-file`) |
| `curl` and `sha256sum`, `shasum` or `openssl` | `make prepare-demo` only |

Setup needs the network. The checks do not:

```sh
make container-build   # network: build targetcheck-dev:go1.27.4 from dev/Containerfile
make prepare-demo      # network: fetch wget-1.21.4.tar.gz into .cache/wget and verify its SHA-256
make test-container    # offline: make verify test-native test-arm demo dist in the image
```

How `make test-container` works:

- It streams `git archive HEAD`, plus the verified Wget tarball, into the
  container. So it tests committed content only.
- It runs the container with `--network=none`. A full run takes about
  8 minutes on a two-CPU Docker VM.
- It brings the results back into a new directory,
  `.cache/test-container/COMMIT.XXXXXX/`, even when a target fails. The
  directory holds the complete log, `exit-status`, the image's tool record,
  the demo evidence and the `dist/` archives. The host then validates the
  `dist/` checksums again.
- Each run keeps its own directory. Remove old ones with
  `rm -rf .cache/test-container`.

To run only some targets, use
`make test-container TEST_CONTAINER_TARGETS='verify test-native'`.
`make verify-container` runs the subset `verify test-native`, and
`make demo-container` runs `verify test-native test-arm demo`.

To run `targetcheck` interactively in the image, start a shell with a checkout
mounted read-only, then build the CLI inside it. On macOS, the checkout must
be in a directory that is shared with the Docker VM, such as your home
directory.

```sh
docker run --rm -it --network=none -v "$PWD:/src:ro" -w /src targetcheck-dev:go1.27.4 bash
go install -buildvcs=false ./cmd/targetcheck
```

`go install` puts `targetcheck` in `/go/bin`, which is on the image's `PATH`,
so the quickstart commands below work unchanged. `-buildvcs=false` is needed
because git refuses a mounted checkout that another user owns. Such a binary
reports `dev commit=unknown`.

## Quickstarts

[examples/quickstart](../examples/quickstart/README.md) compares a defective
baseline and a fixed candidate of a small synthetic component. It runs them
natively, then with a deliberate mismatch, then on ARM32 under QEMU. Each run
copies the example into a new scratch directory, because the build commands
write into the variant directories. The native comparison is:

```sh
work=$(cd "$(mktemp -d)" && pwd -P)
cp -R examples/quickstart "$work/root"
targetcheck check --manifest "$work/root/native.json" --root "$work/root" --output "$work/report"
```

The ARM32 comparison uses `arm.json` instead of `native.json`. The real GNU
Wget case and its seven manifests are in
[example-walkthrough.md](example-walkthrough.md).

## The `targetcheck` command

```text
targetcheck check --manifest FILE --root DIR --output NEW_DIR
targetcheck version
targetcheck help
```

| Option | Meaning |
| --- | --- |
| `--manifest FILE` | an ordinary file of at most 1 MiB that holds one schema-v1 manifest. It can live anywhere |
| `--root DIR` | the input workspace, which holds both variant directories. A relative path is made absolute. The directory and all its ancestors must be real directories, not symlinks |
| `--output NEW_DIR` | the report directory to create. It must not exist, its parent must exist, and no ancestor may be a symlink. It must be outside both variant directories. If the manifest is invalid, it must also be outside `--root` |

### What a run does

`targetcheck check` works through these steps:

1. It loads and validates the manifest.
2. It runs preflight on both variants, without running anything. Preflight
   checks the variant directories, every declared input (and hashes it now),
   the build tools, the QEMU executable and sysroot, any existing artifacts
   and the output destination.
3. It creates the output directory, with an `INCOMPLETE` marker and `logs/`.
4. For a `qemu-arm` target, it runs the configured emulator once as
   `QEMU --version` and records the first non-empty line it prints as the
   `qemu` tool's version. A failed query is recorded and does not stop the run. Apart
   from your declared commands, this is the only process TargetCheck starts
   (see [execution.md](execution.md#qemu-version-query)).
5. For the baseline, it runs the build steps in order, and each must exit 0.
   It records each artifact's identity: its SHA-256, size and ELF details.
   Then it runs the tests in order and records the artifact identities again.
6. It does the same for the candidate.
7. It evaluates every check, writes the logs, `report.json` and `report.md`,
   and removes `INCOMPLETE`.

A test mismatch does not stop the run: the candidate is still built and
tested. An infrastructure failure stops everything after it. Infrastructure
failures are a failed build, a launch failure, a signal, a timeout, an output
limit and a changed artifact. The remaining checks are reported as `not_run`.

### What it prints

After both reports are completely written, TargetCheck prints a three-line
summary on stdout. It never prints raw command output:

```text
targetcheck: matched
JSON: "/tmp/tmp.XXXXXXXXXX/report/report.json"
Markdown: "/tmp/tmp.XXXXXXXXXX/report/report.md"
```

On exit 2 or 130, stderr also gets exactly one line with the report's outcome,
failure kind and message: `targetcheck: error: KIND: MESSAGE` or
`targetcheck: cancelled: MESSAGE`. When a command failed, the message names
its check:

```text
targetcheck: error: missing_tool: QEMU executable is unavailable
targetcheck: error: timeout: baseline test "valid-port": command exceeded its configured timeout
targetcheck: cancelled: run cancelled by caller
```

In two cases only this stderr line appears, without the stdout summary:

- No report could be written. The line then ends with
  `; no report written: REASON`, or the refusal is itself the failure, as in
  `targetcheck: error: invalid_input: output directory already exists`.
- Writing the reports failed. The line then ends with
  `; incomplete output at "DIR"`.

The line shows backslashes, control characters, non-ASCII text and invalid
UTF-8 as Go escapes, such as `\\`, `\n` and `\xff`. So untrusted text cannot
add lines or terminal controls.
[contracts.md](contracts.md#observations-outcomes-and-reports) defines the
format.

The other commands:

- `targetcheck version` prints `targetcheck VERSION commit=COMMIT go=GOVERSION`.
- `targetcheck help`, `-h` and `--help` print the usage text and exit 0.
- A usage error prints a line such as
  `targetcheck: error: invalid_input: check requires --manifest, --root and --output, with no positional arguments`,
  then the usage text, on stderr, and exits 2.

### The command environment

Commands inherit the environment of `targetcheck`, plus their declared
overrides. To run them with a minimal environment, start `targetcheck` with
one, as the Wget demo does:

```sh
env -i PATH=/usr/bin:/bin LC_ALL=C "$(command -v targetcheck)" check ...
```

## Writing a manifest for your component

To compare your own component:

1. Lay out one `--root` with a separate directory per variant, neither inside
   the other. For example, use two git worktrees of your component at the
   baseline and candidate revisions, plus any shared test data.
2. Write the build steps as argv arrays that produce the test executables
   inside each variant directory, such as
   `["./configure", "--host=arm-linux-gnueabihf", "CC=arm-linux-gnueabihf-gcc"]`
   and `["make", "-C", "tests", "unit-tests"]`. The baseline and the candidate
   may use different steps.
3. List as `inputs` the files whose identity matters to your claim, such as
   the changed sources and the build configuration. TargetCheck hashes exactly
   those files, not the whole tree.
4. List the test executables as `artifacts`.
5. Write one test per named case, ideally one process per case, and make each
   print a stable result line. Give the baseline the exact exit code and
   marker of its known failure. Give the candidate exit 0 and a pass marker.
   [Expectations and expected negative tests](#expectations-and-expected-negative-tests)
   explains how to choose them.
6. Run the manifest natively.
7. For ARM32, copy the manifest and change the target and the compiler.

One invocation always compares exactly two variants. So native and ARM32 runs
need separate manifests, and so do a first fix and a complete fix. A partial
fix can be the baseline of a second comparison, with its known failure as the
expected failure, as in
`examples/wget/manifests/b-arm-large-offset-corrected.json`.

TargetCheck does not query build tool versions. To record one, add a build
step such as `["gcc", "--version"]`. The report records its argv, and the
step's log keeps its output.

[A minimal manifest](#a-minimal-manifest) shows every required field.

## Expectations and expected negative tests

Each test states what the baseline and the candidate must do. A check is
`matched` when the program exited normally with exactly `exit_code` and its
output contains every marker you gave. Otherwise its status is one of these:

| Status | When |
| --- | --- |
| `mismatch` | the program exited normally, but with another code, or a marker is missing |
| `error` | the program was killed by a signal (for example a segmentation fault), could not be launched, timed out or hit the output limit, or an artifact changed. This is an infrastructure failure, never a test result, and it stops the run |
| `not_run` | the check did not run |

### Expected failures

An expected failure, or expected negative test, is a baseline expectation with
a nonzero `exit_code`. It documents the known defect. Its marker is what makes
it specific: a missing program, a crash or an unrelated failure cannot match
it.

- A matched expected failure is still reported with its actual nonzero exit.
  TargetCheck never relabels it as a passing program.
- Choose a marker that names the defect, such as an assertion name or a
  sanitizer diagnostic. Avoid a generic word like `error`.

A signal is never a test result. So if the baseline defect crashes the
program, the check is `error` (exit 2), not `matched`. Make such a test report
the defect as a normal nonzero exit with a diagnostic instead. For example,
use an assertion harness that prints a named failure and exits 1. Or configure
a sanitizer to exit, as the Wget example does with `-fsanitize=undefined
-fno-sanitize-recover=undefined`.

### Candidate expectations

The candidate must exit 0. Give it a marker too, such as `PASS valid-port`, so
that a program that exits 0 without running the test does not match.

## Manifest reference

A manifest is one strict JSON object.
[schema/manifest-v1.json](../schema/manifest-v1.json) describes its shape, and
TargetCheck checks further rules on top. TargetCheck rejects:

- unknown keys, and duplicate keys at any depth
- `null` for a required field
- trailing data after the object
- invalid UTF-8 and NUL characters

There is no variable interpolation, no implicit shell and no default value for
a required field.

### A minimal manifest

This manifest builds the quickstart's `port.c` in both variants and runs one
test. It contains every required field:

```json
{
  "schema_version": 1,
  "target": {"kind": "native"},
  "variants": {
    "baseline": {
      "cwd": "baseline",
      "build": [{"argv": ["cc", "-o", "port-check", "port.c"], "timeout_seconds": 120}],
      "inputs": ["port.c"],
      "artifacts": ["port-check"]
    },
    "candidate": {
      "cwd": "candidate",
      "build": [{"argv": ["cc", "-o", "port-check", "port.c"], "timeout_seconds": 120}],
      "inputs": ["port.c"],
      "artifacts": ["port-check"]
    }
  },
  "test_inputs": [],
  "tests": [{
    "id": "reject-out-of-range",
    "argv": ["port-check", "reject-out-of-range"],
    "timeout_seconds": 10,
    "baseline_expect": {"exit_code": 1, "stdout_contains": "FAIL reject-out-of-range"},
    "candidate_expect": {"exit_code": 0, "stdout_contains": "PASS reject-out-of-range"}
  }]
}
```

- `target` says where the tests run. Here they run natively.
- `variants` describes the two builds. Each has a directory (`cwd`), build
  steps, the files to hash (`inputs`) and the executables to test
  (`artifacts`).
- `test_inputs` lists shared files that the tests use. This manifest has none.
- `tests` is the ordered list of tests that both variants share. Each test
  runs the same `argv` in both variant directories and states what each
  variant must do.

[examples/quickstart/native.json](../examples/quickstart/native.json) is the
complete quickstart manifest. The Wget example's
[manifests](../examples/wget/manifests/) show multi-step `configure` and
`make` builds, cross-compilation and sanitizer flags.

### Top level

| Key | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | integer | yes | must be `1` |
| `target` | object | yes | where tests run |
| `variants` | object | yes | exactly `baseline` and `candidate` |
| `test_inputs` | array of paths | yes, may be empty | shared files relative to `--root`, hashed like inputs |
| `tests` | array of 1 to 256 tests | yes | the shared, ordered test list |

### `target`

| Form | Meaning |
| --- | --- |
| `{"kind": "native"}` | run test artifacts directly. No other keys are allowed |
| `{"kind": "qemu-arm", "executable": EXE, "sysroot": DIR}` | run each test as `EXE -L DIR ARTIFACT ARGS...`. Both keys are required |

`executable` is one of:

- an absolute path;
- a relative path that contains `/`, resolved against `--root`;
- a bare name, looked up in `PATH`.

`sysroot` is an absolute path or a path relative to `--root`. It must be a
readable directory. The report records both as resolved absolute paths.
TargetCheck does not install or choose an emulator or a sysroot.

### `variants.baseline` and `variants.candidate`

Both variants have the same four required keys:

| Key | Type | Meaning |
| --- | --- | --- |
| `cwd` | path | the variant directory, relative to `--root` and below it. The two must differ, and neither may contain the other |
| `build` | array of 0 to 64 commands | build steps, run in order in `cwd` on the host. With none, the artifacts must already exist |
| `inputs` | array of at least 1 path | source and configuration files, relative to `cwd`, hashed before any build |
| `artifacts` | array of at least 1 path | the ELF executables to test, relative to `cwd`. They may be absent before the build when build steps are declared |

### Commands

| Key | Type | Required | Meaning |
| --- | --- | --- | --- |
| `argv` | array of strings | yes | the program and its arguments. The first element must be nonempty |
| `env` | object of strings | no | environment overrides for this command only. Names match `[A-Za-z_][A-Za-z0-9_]*` |
| `timeout_seconds` | integer | yes | 1 to 3600 |

A bare `argv[0]` is looked up in the command's `PATH` override, or else in the
inherited `PATH`. A path that contains `/` is relative to `cwd`. Arguments are
passed literally. A shell runs only if you name one, for example
`["sh", "-c", "make && make check"]`, and that is your trusted choice. Reports
record the names of overrides, never their values.

### Tests

| Key | Type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | string | yes | unique, nonempty name |
| `argv` | array of strings | yes | `argv[0]` names a declared artifact, relative to each variant's `cwd`. The rest are literal arguments |
| `timeout_seconds` | integer | yes | 1 to 3600 |
| `baseline_expect` | expectation | yes | what the baseline must do |
| `candidate_expect` | expectation | yes | what the candidate must do. Its `exit_code` must be 0 |

The same `argv` runs in both variants, from each variant's `cwd`. Tests take
no environment overrides.

### Expectations

| Key | Type | Required | Meaning |
| --- | --- | --- | --- |
| `exit_code` | integer 0 to 255 | yes | the exact normal exit status |
| `stdout_contains` | nonempty string | no | a literal byte substring that stdout must contain |
| `stderr_contains` | nonempty string | no | a literal byte substring that stderr must contain |

A nonzero baseline `exit_code` requires at least one marker. Every marker you
give must match.

### Paths and limits

Declared paths are `cwd`, `inputs`, `artifacts`, `test_inputs` and the test
`argv[0]`. They follow these rules:

- They are relative and may not contain `..` components. They are normalized
  lexically.
- A list may not name the same path twice.
- Declared files, and every directory that leads to them, must not be
  symlinks.
- Inputs must be ordinary readable files.

| Limit | Value |
| --- | --- |
| manifest size | 1 MiB |
| tests | 1 to 256 |
| build steps | 64 per variant |
| timeouts | 1 to 3600 seconds per command |
| captured output | 1 MiB of stdout and 1 MiB of stderr per command. More stops the command as `output_limit` |

## Output directory and logs

```text
NEW_DIR/                     mode 0700
├── report.json              mode 0600, authoritative
├── report.md                mode 0600, derived from the same report
├── INCOMPLETE               present only while writing, or after a failed write
└── logs/                    mode 0700
    ├── baseline-build-0.stdout
    ├── baseline-build-0.stderr
    ├── baseline-test-0.stdout
    ├── ...
    └── candidate-test-N.stderr
```

Log names have the form `VARIANT-KIND-INDEX.STREAM`. `KIND` is `build` or
`test`, and `INDEX` is the zero-based position in the manifest's build or test
list.

- Each step that ran has one stdout file and one stderr file. They hold
  exactly the captured bytes, up to 1 MiB per stream. Steps that did not run
  have no logs.
- The report links every log and records its observed byte count and whether
  it was truncated.
- Files are written to a temporary name and then renamed. Only the user who
  ran TargetCheck can read them.

TargetCheck never deletes or reuses existing data, so the output directory
must be new. What you find in it depends on how the run ended:

| How the run ended | What the output directory holds |
| --- | --- |
| preflight passed and the reports were written | `report.json`, `report.md` and the logs of every step that ran, whatever the outcome |
| the manifest or preflight was invalid | an error report and an empty `logs/` |
| writing failed, or the run was interrupted while writing | `INCOMPLETE` stays, and the exit status is 2, or 130 for cancellation. Do not treat such a directory as a completed report |
| the destination itself was the problem: it exists, it is unsafe or it cannot be created | no report. The problem is reported on stderr only, with exit 2 |

Logs, argv and diagnostics can contain sensitive data; see
[limitations.md](limitations.md#data-sensitivity).

## Exit codes

| Exit | Outcome | Meaning |
| --- | --- | --- |
| 0 | `matched` | every build exited 0, every test in both variants matched its expectation, the artifacts did not change, and both reports were written |
| 1 | `mismatch` | at least one test ran normally but did not meet its expectation, with no infrastructure failure or cancellation |
| 2 | `error` | a usage error, an invalid manifest, a preflight failure (missing tool, QEMU or sysroot, unsafe path, invalid artifact), a failed build, a launch failure, a signal, a timeout, an output limit, a changed artifact, an output-directory problem, or a failure to write the reports, even when every test matched |
| 130 | `cancelled` | a signal stopped the run. TargetCheck stopped the running command's process group and finalized both reports |

Which signals cancel a run:

- SIGINT, SIGTERM and SIGHUP cancel a run, with exit 130. A closing terminal
  sends SIGHUP.
- A signal that was already ignored when TargetCheck started stays ignored,
  for example SIGHUP under `nohup`.
- Go keeps such an inherited ignore only for SIGINT and SIGHUP, so SIGTERM
  always cancels.

When several things go wrong, the outcome follows this order of precedence:
cancellation, then infrastructure failure and invalid input, then mismatch,
then matched. So a run with a mismatch and a later timeout exits 2. An
interrupted run exits 130, even if a check had already failed or the reports
could not be written.

`targetcheck version` and `targetcheck help` exit 0.

## Reports

`report.json` follows [schema/report-v1.json](../schema/report-v1.json) and is
the authoritative record. `report.md` renders the same report for reading. It
quotes and escapes every untrusted string, so that names, output or paths
cannot change its structure.

| `report.json` member | Content |
| --- | --- |
| `schema_version` | `1` |
| `tool` | TargetCheck `version`, `commit` and `go_version` |
| `target` | `kind`, `host_os`, `host_arch` and, for QEMU, the resolved `executable` and `sysroot` |
| `manifest` | SHA-256 of the manifest bytes (`raw_json`), or of its canonical JSON for a constructed manifest (`canonical_json`) |
| `inputs` | path (relative to `--root`), SHA-256 and size of every declared input and test input, sorted by path |
| `tools` | the resolved paths of the build tools and, for `qemu-arm`, of the QEMU executable and the sysroot. The `qemu` entry has the version from one `--version` query after preflight. Build tool and sysroot versions are not queried, so their entries carry an explicit `version_error` instead; see [identity limits](limitations.md#identity-limits). None of this attests the full environment |
| `declared` | the build and test IDs declared for each variant, taken from the manifest before anything ran |
| `variants` | per variant: `cwd`, the `build` and `tests` checks, and the `artifacts_before` and `artifacts_after` identities with SHA-256, size, ELF class, machine, endianness and ABI |
| `outcome` | `matched`, `mismatch`, `error` or `cancelled` |
| `failure` | for `error` and `cancelled`: the failure `kind` and `message` |

Each check in `variants` records:

- what was planned: its ID, index, kind, argv, cwd, timeout, environment
  override names and expectation;
- the verdict: its `status` (`matched`, `mismatch`, `not_run` or `error`) and
  a `reason`;
- if it ran, what happened (`observation`): whether it started, the actual
  argv and cwd, the exit code or signal, any failure, the stdout and stderr
  byte counts, truncation flags and log names, and the duration.

The failure kinds are `invalid_input`, `missing_tool`, `build_failure`,
`launch_failure`, `signal`, `timeout`, `cancelled`, `output_limit`,
`identity_change`, `invalid_artifact`, `io` and `not_run`.
[outcomes.md](outcomes.md) and [report-format.md](report-format.md) define the
evaluation and the rendering in detail.

### Sample report

This excerpt is the baseline's second test from the native quickstart, run on
Linux arm64 in the pinned image:

```json
{
  "id": "reject-out-of-range",
  "index": 1,
  "kind": "test",
  "argv": [
    "./port-check",
    "reject-out-of-range"
  ],
  "cwd": "baseline",
  "env_names": [],
  "timeout_seconds": 10,
  "expectation": {
    "exit_code": 1,
    "stdout_contains": "FAIL reject-out-of-range: \"70000\" was accepted as 4464"
  },
  "observation": {
    "started": true,
    "argv": [
      "./port-check",
      "reject-out-of-range"
    ],
    "cwd": "baseline",
    "env_names": [],
    "exit_code": 1,
    "stdout": {
      "bytes": 55,
      "truncated": false,
      "log": "logs/baseline-test-1.stdout"
    },
    "stderr": {
      "bytes": 0,
      "truncated": false,
      "log": "logs/baseline-test-1.stderr"
    },
    "duration_millis": 0
  },
  "status": "matched",
  "reason": "normal exit and all required literal markers matched"
}
```

Here is the same check in `report.md`. Entities such as `&#34;` keep
untrusted text inert:

```markdown
#### Check 1: &#34;reject&#45;out&#45;of&#45;range&#34;

- Kind: &#34;test&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all required literal markers matched&#34;
- Planned argv: &#91;&#34;&#46;&#47;port&#45;check&#34;&#44;&#34;reject&#45;out&#45;of&#45;range&#34;&#93;
- Cwd: &#34;baseline&#34;; timeout seconds: 10
- Environment override names: &#91;&#93;
- Expected normal exit: 1; stdout marker: &#34;FAIL reject&#45;out&#45;of&#45;range&#58; &#92;&#34;70000&#92;&#34; was accepted as 4464&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 0
- Actual argv: &#91;&#34;&#46;&#47;port&#45;check&#34;&#44;&#34;reject&#45;out&#45;of&#45;range&#34;&#93;
- Actual cwd: &#34;baseline&#34;; environment override names: &#91;&#93;
- Actual normal exit: 1
- stdout: observed bytes: 55; truncated: false; log: [stdout log](logs/baseline-test-1.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/baseline-test-1.stderr)
```

Complete reports for other situations are kept as rendering test fixtures in
`internal/render/testdata/`, with synthetic values. There is one for each of
these cases, as `.json` and `.md`: an expected baseline failure
(`matched-negative`), a candidate mismatch, a build failure, a timeout, a
cancelled partial run and an artifact identity change. Real Wget reports come
out of `make demo`; see
[example-walkthrough.md](example-walkthrough.md#reading-the-evidence).

## Versions and compatibility

TargetCheck follows [semantic versioning](https://semver.org/). Within v1.x,
these stay backward compatible:

- the Go API of `pkg/targetcheck`;
- the CLI commands, flags and exit codes;
- the manifest and report formats of `schema_version: 1`.

Minor releases can add features without breaking any of these, and patch
releases fix bugs. An incompatible change needs a new major version, with the
module path `github.com/firmdura/targetcheck/v2`, or a new schema version. The
[CHANGELOG](../CHANGELOG.md) lists the changes in every release.

- **Schemas.** Manifests must say `schema_version: 1`. v1.0.0 accepts only
  version 1 and rejects any other value. Reports carry `schema_version: 1`. An
  incompatible change to either format will use a new schema version, and the
  CHANGELOG will describe the migration.
- **Migration from v0.1.0.** Nothing to change. v1.0.0 behaves the same as
  v0.1.0; only the documentation and the version changed.
