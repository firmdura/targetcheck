# Troubleshooting

When a run does not give the result you expected, work through these steps:

1. **Check the exit status.** Exit 1 means that a test ran and did not meet
   its expectation. Exit 2 means that TargetCheck could not produce a test
   result. Exit 130 means that the run was cancelled.
   [usage.md](usage.md#exit-codes) lists every case.
2. **Read the stderr line.** On exit 2 and 130, stderr has one line:
   `targetcheck: error: KIND: MESSAGE` or `targetcheck: cancelled: MESSAGE`.
   If a command failed, the message names it, as in
   `baseline test "valid-port": ...`.
3. **Open the report.** If the output directory could be created,
   `report.json` has the same `failure.kind` and `failure.message`. The check
   that failed is marked `error`, and the checks after it `not_run`. For a
   mismatch, the check's `reason` says why.
4. **Read the logs.** `logs/` in the output directory holds what each program
   printed.

Then look up the message below. The sections are ordered by failure kind. The
quoted messages are the parts that identify the cause.

## Wrong host or toolchain

**`Use the pinned Linux container`** from a Make target, or
**`supported hosts are Linux amd64 and arm64`** (`invalid_input`) from
`targetcheck check`. TargetCheck runs checks only on Linux amd64 and arm64.
On macOS, use `make test-container`, or run the CLI inside the pinned image
(see [usage.md](usage.md#the-pinned-development-container)).

**`Go 1.27.4 is required.`** from a Make target. The repository targets use
exactly the pinned toolchain. Use the pinned image, or put Go 1.27.4 first on
your `PATH`.

## Missing tool or sysroot

Preflight finds all of these before any command runs. The report then has no
variants or checks, and the exit status is 2.

| Message contains | Kind | Cause and fix |
| --- | --- | --- |
| `baseline build-0 executable is unavailable` (or another variant and step) | `missing_tool` | the build step's `argv[0]` is not on its `PATH`, or is not executable. Install the tool, use its absolute path, or set `PATH` in the step's `env` |
| `QEMU executable is unavailable` | `missing_tool` | the `target.executable` path or name does not resolve to an executable. Install QEMU user mode (Debian package `qemu-user`) or correct the path |
| `QEMU sysroot is unavailable`, for example `QEMU sysroot is unavailable: no such file or directory` | `invalid_input` | the `target.sysroot` path does not resolve, usually because it does not exist. Install the sysroot (the Debian package `libc6-armhf-cross` provides `/usr/arm-linux-gnueabihf`) or correct the path |
| `QEMU sysroot is unreadable`, for example `QEMU sysroot is unreadable: permission denied` | `invalid_input` | the sysroot exists, but the user running TargetCheck cannot open it. Fix its permissions |
| `QEMU sysroot must be a directory` | `invalid_input` | `target.sysroot` names a file, not a directory |

A missing QEMU executable is `missing_tool`, and a missing sysroot is
`invalid_input`. Both exit 2. So you can tell a missing tool, emulator or
sysroot apart from a failing candidate, which exits 1. A missing shared
library is different, as the [next section](#dynamic-loader-and-shared-libraries)
explains.

For a `qemu-arm` target, the report's `tools` has a `qemu` entry. If its
`version_error` starts with `--version query`, the emulator did not report a
version when TargetCheck ran it as `QEMU --version`. That never fails a run.
[execution.md](execution.md#qemu-version-query) lists the possible texts.

`make test-arm` and `make demo` check their tools first. They fail with
`test-arm: FAIL: missing ...` or `demo: missing required tools or ARM sysroot
files: ...`, and they never skip. Install the listed packages or use the
pinned image.

## Dynamic loader and shared libraries

| Symptom | Cause and fix |
| --- | --- |
| `ARM dynamic loader is missing from sysroot` (`invalid_artifact`, exit 2) | a dynamically linked ARM32 artifact names an ELF interpreter, usually `/lib/ld-linux-armhf.so.3`, that is not an executable file inside the sysroot. Point `target.sysroot` at a complete ARM32 hard-float sysroot, or link the artifact statically |
| a test `mismatch` with `exit 127` and `error while loading shared libraries` in its stderr log (exit 1) | the loader started, but a shared library such as `libc.so.6` or `libubsan.so.1` is missing from the sysroot (or, natively, from the host). The program did run, so TargetCheck records a normal exit 127 |
| `cannot launch command` with `no such file or directory` (`launch_failure`, exit 2) for a native test | the kernel could not start the artifact, typically because its ELF interpreter does not exist on the host |

The second case is a test mismatch, not an infrastructure failure, because
TargetCheck cannot tell a loader that exits 127 from a program that exits 127.
Such a run can never satisfy an expected failure that requires a marker. A
candidate expectation with a marker fails visibly too. This is one reason to
give every expectation a marker. Before you conclude that the candidate
regressed, check the stderr log of the mismatching check.

## Wrong artifact target

| Message contains | Cause and fix |
| --- | --- |
| `ELF does not match native Linux target` | a `native` artifact is not an ELF64 executable for this host's architecture. It was built for another target |
| `ELF is not ARM32 EABI5 hard-float` | a `qemu-arm` artifact is not little-endian ARM32 EABI5 hard-float, for example a native or soft-float build |
| `invalid existing artifact` | an artifact that was present before the build already has the wrong format, for example a native `port-check` left in the directory before an ARM32 run. Use a fresh copy of the workspace, or remove stale build outputs |
| `artifact is not executable` | the declared artifact lacks execute permission |

## Permissions

| Message contains | Cause and fix |
| --- | --- |
| `io: create new output` with `permission denied` | the parent of `--output` is not writable. Choose a writable parent. Nothing ran, and no report was written |
| `declared input ... is unreadable or unsafe: permission denied` | the user running TargetCheck cannot read a declared input |
| a `build_failure` whose stderr log says `Permission denied` | the build could not write into the variant directory. Build steps run with your permissions and need write access there |
| `input workspace is not an ordinary directory or traverses a symlink` | `--root` or one of its ancestors is a symlink, as `/tmp` is on macOS. Pass the physical path, for example `--root "$(cd DIR && pwd -P)"` |

Reports and logs are readable only by their owner (directories 0700, files
0600). To share them, review them for sensitive data first, then copy them or
change their permissions. Files that a root container writes into a
bind-mounted directory are owned by root on the host. `make test-container`
avoids this by streaming the evidence out instead.

## Timeouts and cancellation

**`command exceeded its configured timeout`** (`timeout`, exit 2), for example
`baseline test "valid-port": command exceeded its configured timeout`. The
named build or test ran longer than its `timeout_seconds`. TargetCheck stopped
its process group with TERM, then KILL after 250 ms, and the later checks are
`not_run`. Raise the timeout, up to the maximum of 3600 seconds per command.
Emulated ARM32 tests and sanitizer builds are slower than native ones.

**`targetcheck: cancelled: run cancelled by caller`** (exit 130). SIGINT,
SIGTERM or SIGHUP reached `targetcheck`, or a library caller's context was
cancelled or reached its deadline. TargetCheck keeps the completed checks,
stops the running command and marks the rest `not_run`.

A signal that was already ignored when TargetCheck started stays ignored, but
only SIGINT and SIGHUP can stay ignored ([usage.md](usage.md#exit-codes) has
the rules). So if SIGHUP does not stop a run under `nohup`, or SIGINT does not
stop a background job that a non-interactive shell started, send SIGTERM. It
always cancels.

Cancellation during preflight hashing takes effect only after the current
file has been hashed. With a 3 GiB input, the exit came about 0.9 seconds
after SIGINT.

## Failed build

**`build "build-N": exit X; expected 0`** (`build_failure`, exit 2), for
example `candidate build "build-0": exit 1; expected 0`. A build step exited
nonzero. Every build step must exit 0, and nothing runs after a failed build.
To find the cause:

1. Read `logs/VARIANT-build-N.stderr` and `.stdout` in the output directory,
   for example `cat "$out/logs/candidate-build-0.stderr"`.
2. Reproduce the step: run its `argv` in its `cwd`, with the same environment
   overrides.

**`command exceeded the stdout or stderr capture limit`** (`output_limit`,
exit 2). A step printed more than 1 MiB on one stream, and the log keeps the
first 1 MiB. Make the step quieter, for example by redirecting verbose output
to a file inside an explicit shell step.

## Manifest and output errors

A manifest error exits 2 before any command runs. Typical messages are:

- `unknown field "extra"`
- `duplicate key "schema_version"`
- `missing or null field "test_inputs"`
- `unsupported schema_version`
- `empty, invalid or duplicate test ID`
- `nonzero baseline expectation requires literal marker`
- `invalid baseline or candidate exit expectation`
- `test executable is not a declared artifact`
- `parent path component is forbidden`

Compare the manifest with the [reference](usage.md#manifest-reference).

| Message contains | Fix |
| --- | --- |
| `output directory already exists` | choose a new `--output`. TargetCheck never reuses or deletes existing data |
| `output directory is inside a variant workspace` | put the reports outside both variant directories |
| `no report written: failure reports for an invalid manifest must be outside the input workspace` | with an invalid manifest, TargetCheck cannot tell where the variants are, so `--output` must be outside `--root`. The line starts with the manifest error |
| `inspect output parent` with `no such file or directory` | the parent of `--output` must already exist |
| `overlap; variant directories must differ and neither may contain the other` | the two variant `cwd`s are equal, or one contains the other. A build could then rewrite the other variant's inputs. Use sibling directories such as `baseline` and `candidate` |
| `artifact identity changed during testing` (`identity_change`), for example `baseline test "t1": artifact identity changed during testing: baseline/runner` | a test, or another process, modified a declared artifact while it was tested. The result cannot be attributed to one binary |

An `INCOMPLETE` file in the output directory means that writing the report or
the logs failed or was interrupted. The exit status was then 2, or 130, and
stderr ended with `; incomplete output at "DIR"`, as in
`targetcheck: error: io: output persistence failed: ...: permission denied; incomplete output at "DIR"`.
Do not use such a directory as a completed report.

## Repository targets

**`detected dubious ownership`** or **`error obtaining VCS status`**. git
refuses a checkout that another user owns, for example a bind mount in a root
container. The Make targets disable Go's VCS stamping, so `make verify`,
`test-native`, `test-arm` and `demo` work there. Your own `go build` needs
`-buildvcs=false`. `make dist` needs the commit, so it fails with
`git cannot read this checkout`. Use `make test-container`, or run
`git config --global --add safe.directory /src` in a throwaway container that
you control.

**`prepare-demo: refusing ...: SHA-256 ... does not match the pinned ...`**.
The cached Wget tarball is corrupt or not the pinned file. It was not
extracted or used. Delete `.cache/wget/wget-1.21.4.tar.gz` and run
`make prepare-demo` again.

**`the pinned image targetcheck-dev:go1.27.4 is missing`** from
`make test-container`. Run `make container-build` (network) first. A missing
Wget cache stops it with `run make prepare-demo (network) first`.

**`dist: FAIL: ...`**. `make dist` names the check that failed. Before it
builds anything, it requires its own tools and those of its smoke tests,
which build and run the bundled native and ARM32 quickstarts:

- `cc`, `arm-linux-gnueabihf-gcc` and `qemu-arm`;
- the sysroot files `/usr/arm-linux-gnueabihf/lib/ld-linux-armhf.so.3` and
  `lib/libc.so.6`;
- `qemu-x86_64` or `qemu-aarch64`, to run the other architecture's binary.

A missing one fails with `missing required tools or ARM sysroot files: ...`
or `... is required to smoke-test the linux/ARCH CLI`. Install it or use the
pinned image. Later checks name the archive and the property that failed,
such as a `CHANGELOG.md` without an entry for `VERSION`.
