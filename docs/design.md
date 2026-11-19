# TargetCheck design and verification

TargetCheck runs the same component tests against a baseline and a candidate.
Both use one ordered list of tests. Each build has its own expected outcomes,
so a known baseline failure can be distinguished from an unexpected candidate
failure. The report ties each observation to the binary that ran.

The host is Linux amd64 or arm64. Tests run natively or on little-endian ARM32
hard-float binaries under a caller-supplied `qemu-arm` and sysroot. Each run
compares one pair of builds. The caller supplies the source, trusted build
commands, test cases, and tools. Firmware deployment, hardware qualification,
and selecting or backporting security fixes are outside this tool's scope.
See the [platform and trust limits](limitations.md) and the
[Apache-2.0 and Wget licensing details](../README.md#license).

## How a comparison runs

1. **Load the manifest.** A strict JSON manifest declares the target, the
   baseline and candidate directories, their inputs and artifacts, and the
   shared tests. Both variants run the same test arguments. Each expectation
   specifies an exit code and any output markers that must match. A nonzero
   baseline expectation requires a marker. Candidate tests must exit zero.
2. **Check both workspaces before launching a command.** TargetCheck resolves
   tools and paths, rejects invalid declarations, and hashes the manifest and
   declared inputs. The limits are 1 MiB per manifest, 256 tests, and 64 build
   steps per variant. Each command has a timeout of 1 to 3600 seconds and
   separate 1 MiB limits for stdout and stderr.
3. **Build and test the baseline, then the candidate.** Build commands run on
   the host. ARM tests use the specified emulator and sysroot. A baseline
   mismatch still allows the candidate tests to run. A build or infrastructure
   failure stops the run. The runner hashes each artifact before and after
   testing. A changed identity is an infrastructure failure.
4. **Record what ran.** JSON contains the observed arguments, tool paths, file
   digests, process outcomes, and checks left unrun. Markdown summarizes that
   record. The CLI writes both reports and per-step logs to a new directory.
   It exits 0 for a match, 1 for a mismatch, 2 for an input, infrastructure,
   or output error, and 130 for cancellation.

The [component contracts](contracts.md) define the manifest and API types,
validation rules, identity checks, and error precedence. The
[Go API](library.md) exposes the same comparison and report writers. The
[report reference](report-format.md) describes the evidence recorded for a
partial run.

## Verification

The [pinned Linux image](../dev/Containerfile) supports local and CI runs.
[Tool versions and checksums](../dev/tool-versions.txt) identify the Go
toolchain, native compiler, ARM cross-compiler, sysroot, and QEMU used for
the reference environment.

| Target | Coverage |
| --- | --- |
| `make verify` | Formatting, vet, unit and race tests, package and CLI builds |
| `make test-native` | Native process and CLI integration tests |
| `make test-arm` | Cross-built ARM32 binaries under QEMU. A missing emulator or toolchain fails the target. |
| `make demo` | Native and ARM32 results for the Wget example |
| `make dist` | Linux amd64 and arm64 release archives and SHA-256 checksums |

`make prepare-demo` downloads the pinned Wget source and verifies its SHA-256
before extraction. After preparation, `make test-container` runs the suite
with networking disabled. The [v1.0.1 release notes](https://github.com/firmdura/targetcheck/releases/tag/v1.0.1)
link the CI run and record the checksum comparison between local arm64-host
and CI amd64-host builds.

The Wget example recorded a native sanitizer failure in the baseline. The
first upstream fix passed the large-offset tests natively and failed the
same subset under ARM32 QEMU. The candidate with the follow-up changes passed
the scoped tests on both. The [results](example-walkthrough.md#expected-results)
and [known limits](limitations.md#the-wget-cve-2026-58470-example) describe
the tested cases and the remaining limitation.

## References

- [QEMU user-mode execution](https://www.qemu.org/docs/master/user/main.html)
- [Linux backport verification](https://docs.kernel.org/process/backporting.html)
- [Ubuntu advisory for the Wget example](https://ubuntu.com/security/CVE-2026-58470)
- [GNU Wget 1.21.4 source](https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz), SHA-256 `81542f5cefb8faacc39bbbc6c82ded80e3e4a88505ae72ea51df27525bcde04c`
- [First upstream change](https://gitlab.com/gnuwget/wget/-/commit/43d3ba9336bc94937e6fae2365c6ffd30c34ffcf), [32-bit follow-up](https://gitlab.com/gnuwget/wget/-/commit/01ff771caac1958662ca8665eed2021ec386a7af), and [upstream credits and local changes](../examples/wget/NOTICE)
