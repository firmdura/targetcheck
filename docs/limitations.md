# Limitations and scope

TargetCheck answers one narrow question: did the identified binaries do what
your tests expected? This page explains what that answer does not cover. It
also describes the trust model, how to handle the data in reports, and the
known limits.

## What a result means

`matched` means that every declared test met its expectation, under these
conditions:

- for the binaries whose SHA-256 identities the report records;
- built by the recorded commands from the declared inputs;
- in the environment where TargetCheck ran.

A matched expected failure means that the baseline failed in exactly the
declared way.

A result does not mean that a vulnerability is fixed, that a CVE does or does
not apply, or that every path to a defect is repaired. Tests show only what
they test. TargetCheck is not a security-fix certification.

## Trusted commands; not a sandbox

Build and test commands are arbitrary programs. They run with your user's
permissions, your filesystem and network access, and the environment of the
`targetcheck` process plus the declared overrides. TargetCheck does not
isolate or restrict them. They can read and write anything you can, including
files outside `--root`. They can make network requests and start other
processes.

- TargetCheck itself never deletes or rewrites workspace data, and it never
  makes network requests. Your commands can do both. Build commands normally
  write into their variant directory.
- Besides your commands, TargetCheck starts exactly one process. For a
  `qemu-arm` target, after preflight and before the first build, it runs the
  configured emulator as `QEMU --version` in `--root`, with the inherited
  environment and a 10 second timeout. Point `target.executable` only at an
  emulator you trust.
- On a timeout, a cancellation or an output-limit stop, TargetCheck
  terminates the command's process group and reaps it. A program that leaves
  its process group, for example by daemonizing, can outlive the run.
- QEMU user mode is an emulator, not an isolation boundary. An emulated
  program's system calls go to the host kernel, with your permissions.
- Run only manifests and sources that you trust. Use a disposable container
  or virtual machine for code that you do not trust.

## Data sensitivity

`report.json`, `report.md` and the files in `logs/` contain the exact argv of
every command, the working directories, the resolved tool paths, diagnostics
and the raw stdout and stderr of every step. Any of these can contain
secrets: a token passed as an argument, credentials printed by a build tool,
internal host names or source excerpts. TargetCheck redacts nothing.

- Reports record the names of the manifest's environment overrides, never
  their values. The inherited environment is not recorded. Commands still
  receive the whole environment, though, and can print it into the logs.
- The manifest itself contains the override values. Treat a manifest that
  holds secrets as sensitive. The report records only its SHA-256.
- Output directories are created readable only by their owner (directories
  0700, files 0600). Review them before you share them.
- Captured output is held in memory until it is written: up to 1 MiB per
  stream per command. In the worst case, that is 640 commands (64 build steps
  and 256 tests per variant) times 2 MiB.

## Identity limits

SHA-256 identities cover exactly the declared files:

- the manifest bytes;
- the declared inputs and shared test inputs, hashed before any build;
- the declared artifacts, inspected immediately before and after each
  variant's tests, together with their ELF class, machine, endianness and
  ABI.

They do not cover undeclared sources or headers, libraries loaded at run
time, the compiler and build tools, the sysroot, QEMU, the kernel or the
environment. What the reports do record about those:

- the resolved paths of the build tools, the QEMU executable and the sysroot;
- the QEMU version from one `--version` query after preflight. This is the
  emulator's own statement, not an attestation of its build.

Build tool and sysroot versions are not queried, so their entries carry an
explicit `version_error`. To keep a toolchain version in the evidence,
declare a build step such as `["gcc", "--version"]`. The sysroot's contents
are not hashed. None of this attests the full environment.

More limits:

- A digest identifies bytes. It does not show who produced them or that they
  are trustworthy. It is not provenance or attestation.
- Inherited `QEMU_*` environment variables, and the inherited environment in
  general, are not recorded. They can still change the behavior of the
  emulator and of the commands.
- An artifact that changes during testing is an `identity_change` failure.
  TargetCheck does not watch other files.
- Reported tool and target paths can depend on where `--root` is, when a
  tool, the QEMU executable or the sysroot lies inside it; see
  [contracts.md](contracts.md#identity-and-execution-plan).

## Emulator component tests versus physical-product qualification

`qemu-arm` user mode runs a single ARM32 Linux user-space program on the host
kernel. It emulates the CPU and uses the libraries of the supplied sysroot. A
component test there exercises:

- the component's code, compiled for ARM32;
- its ARM32 type sizes and ABI;
- the C library of the supplied sysroot.

It does not boot a kernel, a firmware image, a bootloader or drivers. It does
not reproduce a device's kernel, C library build, CPU model and errata, memory
layout, timing, peripherals, storage, networking, power behavior or update
mechanism.

A passing ARM32 component test is evidence about that code under that
emulated ABI. To qualify a physical product, you test the built firmware on
the device, with its configuration. That is outside TargetCheck.

## Platform and target limits

- Hosts: Linux amd64 and arm64 only. macOS and Windows users run the Linux
  container. The v1.0.0 acceptance evidence was produced on Linux arm64 in the
  pinned image.
- Targets: native, and little-endian ARM32 EABI5 hard-float through
  `qemu-arm`. Other targets are not supported, such as soft-float or
  big-endian ARM, AArch64 under QEMU, MIPS or RISC-V.
- Each invocation compares one baseline and one candidate. Builds always run
  on the host.

## Known limits

- **A missing shared library looks like a test failure.** If the sysroot (or,
  natively, the host) lacks a library that the artifact needs, such as
  `libc.so.6`, the dynamic loader exits 127. TargetCheck reports that as a
  test mismatch (exit 1), not as an infrastructure failure (exit 2). It can
  never satisfy an expected failure that requires a marker. Give expectations
  markers, and check the stderr log of a mismatch; see
  [troubleshooting.md](troubleshooting.md#dynamic-loader-and-shared-libraries).
- **A candidate expectation without a marker accepts any program that exits
  0.** That includes a misconfigured emulator or a test that did nothing.
  Give candidate expectations a `stdout_contains` or `stderr_contains` marker
  too.
- **Signals are never test results.** A crashing baseline is reported as an
  infrastructure failure (exit 2), not as a matched expected failure; see
  [usage.md](usage.md#expectations-and-expected-negative-tests).
- **Cancellation during preflight hashing** takes effect after the current
  file has been hashed. A 3 GiB input exited 130 about 0.9 seconds after
  SIGINT.
- **A broken console pipe can still end the CLI.** Suppose stdout or stderr
  is a pipe whose reader has exited, for example a `| tee` killed by the same
  hangup. Then the final console write can terminate `targetcheck` with
  SIGPIPE. That happens after the reports are finalized and the command
  process groups are stopped.
- **QEMU prerequisites have different failure kinds.** A missing sysroot is
  `invalid_input`, and a missing QEMU executable is `missing_tool`. Both
  exit 2.
- **Tool order is not declaration order.** `tools` entries are sorted by
  their role text, so `baseline/build-10` sorts before `baseline/build-2`.
  The order is stable.
- **Preflight-failure reports are sparse.** They record the failure, but not
  the target kind, the manifest digest or the declared coverage.
- **Raw Markdown is entity-escaped.** `report.md` escapes punctuation as HTML
  entities, so that untrusted text cannot change its structure. View it
  rendered. `report.json` is authoritative.
- **Public Go types are aliases** of internal types, so `go doc` and the
  package's pkg.go.dev page show them without their fields.
  [library.md](library.md#types) lists the fields.

## Out of scope

TargetCheck does not:

- decide whether a CVE applies;
- select, download or backport patches;
- run arbitrary remote commands;
- install SDKs or tools;
- extract firmware or provide build adapters;
- boot complete images;
- qualify hardware, drivers or updates;
- sign or deploy firmware;
- provide dashboards, vendor-specific SDKs or external vulnerability
  services;
- run on Windows or macOS.

TargetCheck makes no claim that hashes prove trusted provenance, or that
passing tests prove that every attack path is repaired.

## The Wget CVE-2026-58470 example

[examples/wget](../examples/wget/README.md) tests real GNU Wget 1.21.4 code,
but its claim is just as narrow. For the identified binaries, Wget's
production `parse_content_range()` meets the declared expectations of the
named cases:

- the unpatched parser's signed-integer overflow under
  UndefinedBehaviorSanitizer;
- the first upstream fix's 32-bit large-offset regression on ARM32 under
  `qemu-arm`;
- the complete candidate passing the shared scoped boundary and compatibility
  suite natively and on ARM32.

What the example does not show:

- It does not show complete Wget security, exposure or repair in any product,
  a vendor feature configuration, firmware qualification or device behavior.
  The build uses a minimal `configure` configuration, sufficient for the
  component tests. ARM32 runs use the Debian cross sysroot under QEMU user
  mode, not a target board.
- The local range-length guard
  (`examples/wget/patches/0003-local-reject-overflowing-range-length.patch`)
  is a local addition pending upstream review. It is not upstream-approved.
  It covers only `last_byte_pos - first_byte_pos + 1` with a zero first
  position.
- **Residual:** `gethttp()` also computes `contlen + contrange`, which still
  overflows for `bytes R-9223372036854775807/*` when `R` is a nonzero resume
  offset (`--start-pos=R` or `-c`). This was found by code reading of
  `src/http.c` only, and these tests do not exercise it. The complete
  candidate is therefore not a complete repair of caller-side overflow.
- The hashes identify the tested files. They do not prove the provenance of
  the toolchain or the host.

## Licensing

TargetCheck is Apache-2.0. Its release binaries also contain the Go runtime
and standard library under Go's BSD-style license, which ships in each
archive under `third_party/go/`. The Wget-derived patches in `examples/wget/`
are GPL-3.0-or-later, with the upstream `COPYING` and `NOTICE`. They are not
Apache-2.0, they are never compiled into or linked with TargetCheck, and they
are not in the release archives.
