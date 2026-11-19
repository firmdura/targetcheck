# TargetCheck

You patched a component. Did the fix actually work in the binaries you ship,
natively and on ARM32?

TargetCheck answers that question with your own tests. You give it two
variants of the component: the baseline, before the fix, and the candidate,
with the fix. TargetCheck builds both, runs the same tests against each, and
compares every result with what you said should happen. Tests run natively on
Linux, or as ARM32 binaries under QEMU user mode. You get evidence you can
review: a JSON report, a Markdown report, a log of every step and an exit
status.

A passing result means that the identified binaries did what your tests
expected. It is not a security-fix certification;
[docs/limitations.md](docs/limitations.md) explains what a result does and
does not show.

TargetCheck v1.0.1 is a command-line tool and a Go library for Linux amd64 and
arm64 hosts. Its Go code is licensed under [Apache-2.0](LICENSE).

## A short example

The [quickstart](examples/quickstart/README.md) is a tiny C component with a
port-number parser. Its baseline accepts the out-of-range port 70000, and its
candidate rejects it. One JSON manifest says how to build both variants and
which tests to run. This test, from
[examples/quickstart/native.json](examples/quickstart/native.json), expects
the baseline to fail in one exact way and the candidate to pass:

```json
{
  "id": "reject-out-of-range",
  "argv": ["port-check", "reject-out-of-range"],
  "timeout_seconds": 10,
  "baseline_expect": {
    "exit_code": 1,
    "stdout_contains": "FAIL reject-out-of-range: \"70000\" was accepted as 4464"
  },
  "candidate_expect": {
    "exit_code": 0,
    "stdout_contains": "PASS reject-out-of-range"
  }
}
```

To run the comparison, copy the example to a scratch directory and point
`targetcheck` at it. Run this from an extracted release directory or a source
checkout, with `targetcheck` on your `PATH` and a C compiler named `cc`:

```sh
work=$(cd "$(mktemp -d)" && pwd -P)
cp -R examples/quickstart "$work/root"
targetcheck check --manifest "$work/root/native.json" --root "$work/root" --output "$work/report"
```

TargetCheck builds both variants, runs both tests in each, writes the reports
and exits 0:

```text
targetcheck: matched
JSON: "/tmp/tmp.XXXXXXXXXX/report/report.json"
Markdown: "/tmp/tmp.XXXXXXXXXX/report/report.md"
```

If the candidate still accepted 70000, the run would print
`targetcheck: mismatch` and exit 1. The candidate's check in `report.json`
would say why:

```json
"status": "mismatch",
"reason": "exit 1; expected 0; stdout missing literal marker \"PASS reject-out-of-range\""
```

The exit status tells you what kind of result you got:

| Exit | Outcome | Meaning |
| --- | --- | --- |
| 0 | `matched` | every test in both variants met its expectation |
| 1 | `mismatch` | at least one test ran normally but did not meet its expectation |
| 2 | `error` | there is no complete result, for example because the manifest was invalid, a tool was missing, a build failed, a test crashed or timed out, or the reports could not be written |
| 130 | `cancelled` | the run was interrupted |

[docs/usage.md](docs/usage.md#exit-codes) has the full rules.

## Install

Prebuilt static binaries for Linux amd64 and arm64 are attached to the
[v1.0.1 release](https://github.com/firmdura/targetcheck/releases/tag/v1.0.1).
Download one, check it and unpack it:

```sh
version=v1.0.1
arch=amd64 # or arm64
curl -fLO "https://github.com/firmdura/targetcheck/releases/download/$version/targetcheck-$version-linux-$arch.tar.gz"
curl -fLO "https://github.com/firmdura/targetcheck/releases/download/$version/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf "targetcheck-$version-linux-$arch.tar.gz"
cd "targetcheck-$version-linux-$arch"
sha256sum -c SHA256SUMS
./targetcheck version
```

Each archive contains the CLI, this README, `LICENSE`, the Go toolchain
license for the Go runtime linked into the CLI, `CHANGELOG.md`, `docs/`, the
JSON schemas and the [quickstart example](examples/quickstart/README.md). It
also has a `SHA256SUMS` of its own files.

With Go 1.27.4 or later, you can build the CLI from the module instead:

```sh
go install github.com/firmdura/targetcheck/cmd/targetcheck@v1.0.1
```

To use the library, add the module and import
`github.com/firmdura/targetcheck/pkg/targetcheck`:

```sh
go get github.com/firmdura/targetcheck@v1.0.1
```

The API reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/firmdura/targetcheck/pkg/targetcheck),
and [docs/library.md](docs/library.md) is the guide.

`targetcheck check` and `targetcheck.Run` run only on Linux amd64 and arm64.
On macOS, use the
[pinned Linux container](docs/usage.md#the-pinned-development-container).

## What TargetCheck checks

- **One manifest for both variants.** A strict JSON manifest
  (`schema_version: 1`) names each variant's directory, its trusted build
  commands, the source and configuration files that matter, and the
  executables to test. Both variants share one ordered list of tests.
- **Preflight checks.** Before any command runs, TargetCheck checks both
  variants: paths, symlinks, the declared files, the build tools, the QEMU
  executable and the sysroot. This step is called preflight.
- **Runs commands as written.** It builds and tests the baseline, then the
  candidate. Each command runs directly from its argument list, without an
  implicit shell. Each has its own timeout, and stdout and stderr each have a
  1 MiB capture limit. When a command must stop, TargetCheck stops its whole
  process group.
- **Native or ARM32.** Tests run natively, or as little-endian ARM32 EABI5
  hard-float binaries through `qemu-arm -L SYSROOT`, with the emulator and
  sysroot you configure. EABI5 hard-float is the ABI that Debian calls armhf.
- **Exact expectations.** A test expects an exact exit code and, optionally,
  a marker: literal text that its output must contain. The baseline may be
  expected to fail, but only with an exact nonzero exit code plus a marker.
  The candidate must exit 0. Signals, timeouts, launch failures and
  output-limit stops never satisfy an expectation.
- **Identities and evidence.** It records the SHA-256 of the manifest and of
  the declared inputs. It hashes the tested artifacts too, immediately before
  and after testing. It also records the resolved tool paths, the emulator's
  reported version, the exact arguments of every command and what each
  command did. It writes `report.json`, the authoritative record,
  `report.md`, a readable rendering, and a stdout and a stderr log for every
  step.
- **A Go library with the same semantics:**
  [`pkg/targetcheck`](https://pkg.go.dev/github.com/firmdura/targetcheck/pkg/targetcheck).

TargetCheck does not decide whether a CVE applies, select or backport patches,
install SDKs or tools, download anything, boot firmware images or qualify
hardware. It does not sandbox the commands it runs. See
[docs/limitations.md](docs/limitations.md).

## The real case: GNU Wget CVE-2026-58470

[examples/wget](examples/wget/README.md) applies TargetCheck to public GNU
Wget 1.21.4 source. Its tests call Wget's own `parse_content_range()` through
Wget's unit-test program. It compares the unpatched code, the upstream fix
[43d3ba9](https://gitlab.com/gnuwget/wget/-/commit/43d3ba9336bc94937e6fae2365c6ffd30c34ffcf)
alone, and a complete candidate. The complete candidate adds the 32-bit
follow-up
[01ff771](https://gitlab.com/gnuwget/wget/-/commit/01ff771caac1958662ca8665eed2021ec386a7af)
and a separately labelled local guard. The evidence shows three things:

1. Natively, the unpatched code has a signed-integer overflow, which
   UndefinedBehaviorSanitizer reports.
2. The first fix alone passes a large-offset subset natively, but fails it on
   ARM32 under `qemu-arm`.
3. The complete candidate passes the shared scoped boundary and compatibility
   suite natively and on ARM32.

These are scoped component tests, and the ARM32 runs use user-mode emulation.
They do not show that Wget as a whole is secure, or how any device behaves.
The local guard also has a documented residual, so the complete candidate is
not a complete repair of caller-side overflow.
[docs/limitations.md](docs/limitations.md#the-wget-cve-2026-58470-example)
describes these limits. The [walkthrough](docs/example-walkthrough.md) runs
the example from a macOS or Linux Docker host.

## Documentation

- [docs/usage.md](docs/usage.md): installation, prerequisites, the `check`
  command, writing a manifest, the manifest reference, expectations, output,
  exit codes, sample reports and the version policy
- [docs/library.md](docs/library.md): the Go API, error handling and
  cancellation
- [docs/troubleshooting.md](docs/troubleshooting.md): missing tools or sysroot,
  the dynamic loader, permissions, timeouts and failed builds
- [docs/limitations.md](docs/limitations.md): what a result means, the trust
  model, data sensitivity, identity limits, emulation versus device
  qualification and out-of-scope claims
- [docs/example-walkthrough.md](docs/example-walkthrough.md): the Wget case
  step by step
- Reference: [design and verification](docs/design.md),
  [component contracts](docs/contracts.md),
  [input validation](docs/input-validation.md),
  [execution](docs/execution.md), [outcomes](docs/outcomes.md),
  [report format](docs/report-format.md) and the
  [manifest](schema/manifest-v1.json) and [report](schema/report-v1.json)
  JSON schemas
- [CHANGELOG.md](CHANGELOG.md)

The [v1.0.1 verification run](https://github.com/firmdura/targetcheck/actions/runs/44266791697)
records the release checks. Report problems in the
[issue tracker](https://github.com/firmdura/targetcheck/issues).

## Development and acceptance

The repository's checks run in a digest-pinned Linux image
([dev/Containerfile](dev/Containerfile), versions in
[dev/tool-versions.txt](dev/tool-versions.txt)). It has Go 1.27.4, GCC 12.2,
the Debian `arm-linux-gnueabihf` cross toolchain and sysroot, and QEMU 7.2.
From a Linux or macOS Docker host, run:

```sh
make container-build   # network: build the pinned image
make prepare-demo      # network: fetch the Wget 1.21.4 tarball and verify its SHA-256
make test-container    # offline: verify, test-native, test-arm, demo and dist
```

| Target | What it runs |
| --- | --- |
| `make verify` | gofmt check, `go vet ./...`, `go test -race ./...` and the library and CLI build |
| `make test-native` | real compiled native helpers, the library and the CLI as a subprocess |
| `make test-arm` | cross-built ARM32 binaries under real `qemu-arm` through the CLI |
| `make prepare-demo` | the only network step: fetch the pinned Wget source and check its SHA-256 |
| `make demo` | the Wget native and ARM32 scenario matrix, run twice and compared |
| `make dist` | reproducible Linux amd64 and arm64 archives and `SHA256SUMS` in `dist/`, extracted, checked and smoke-tested |
| `make test-container` | `verify`, `test-native`, `test-arm`, `demo` and `dist` from a clean committed snapshot with `--network=none` |

`verify`, `test-native`, `test-arm`, `demo` and `dist` need Linux and Go
1.27.4, and they run offline. A missing tool or sysroot is a failure, never a
skip. `make test-container` brings the complete log, the demo evidence and the
`dist/` archives back to `.cache/test-container/`. CI runs the same targets.

## About Firmdura

This repository contains a standalone verification utility from
[Firmdura](https://firmdura.com). Firmdura's commercial offer is shared embedded
Linux security maintenance through versioned security layers or patch overlays
for agreed user-space components.

## License

TargetCheck's code, documentation, scripts and examples are licensed under the
[Apache License 2.0](LICENSE), except the Wget-derived files described below.
The release binaries also contain the Go runtime and standard library. Their
BSD-style license and patent grant ship in each archive as
`third_party/go/LICENSE` and `third_party/go/PATENTS`.

In [examples/wget](examples/wget/README.md), the patches are covered by the
GNU General Public License version 3 or later. They are two upstream GNU Wget
commits and two local modifications of Wget. `COPYING` is the unchanged GPL
text from the Wget 1.21.4 tarball. [NOTICE](examples/wget/NOTICE) keeps the
upstream copyright, license and authorship notices and lists the local
modifications.

These Wget-derived files are not Apache-2.0. They are never compiled into or
linked with TargetCheck, and they are not part of the release archives. The recipe
files around them are Apache-2.0, as listed in that directory's README. The
Wget source itself is not redistributed; `make prepare-demo` fetches it from
`ftp.gnu.org`.
