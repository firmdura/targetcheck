# TargetCheck component contracts

This reference specifies the contracts that the CLI and library components
share, the schema fixtures and the Linux verification environment. In short:

- The CLI and the library use the same model, observations, expectations and
  outcomes.
- A manifest is strictly validated, and both variants (the baseline and
  candidate builds) are preflighted, that is checked without running any
  program, before the first command runs.
- Commands run from their argv, never through an implicit shell.
- SHA-256 identities cover the manifest and the declared files only.
- A check without an observation is `not_run`, and empty evidence is never
  success.
- The aggregate outcome follows a fixed precedence and maps to one CLI exit
  code: 0, 1, 2 or 130.

The [design and acceptance criteria](design.md) describe the release scope
and acceptance criteria; the user guides are linked from the
[README](../README.md).

Original TargetCheck code is licensed under [Apache-2.0](../LICENSE). Wget-derived
source, patches and tests belong separately under `examples/wget/`, retain their
GPL license, notices, upstream authorship and source references, and are not
linked into the Go tool. The contract fixtures in `testdata/contracts/` are
synthetic schema examples, not execution or Wget acceptance evidence.

## Public API and shared types

The module is `github.com/firmdura/targetcheck`; consumers import
`github.com/firmdura/targetcheck/pkg/targetcheck`. Public types are aliases of the
single definitions in `internal/model`, so the CLI and library use the same
observations, expectations and outcomes.

The public package implements these entry points:

```go
Load(io.Reader) (Spec, error)
Run(context.Context, Spec, Options) (Report, error)
WriteJSON(io.Writer, Report) error
WriteMarkdown(io.Writer, Report) error
```

`Options.Root` is an explicit absolute workspace directory. Optional
`Options.OutputDir` lets preflight reject unsafe CLI destinations; it does not
grant the library permission to write reports. `Run` returns data, leaves the
caller working directory and environment unchanged, and never calls `os.Exit`,
installs tools or downloads files. Configured commands remain trusted programs
with the caller's permissions.

`RunError` has an `ErrorKind`, a structured `Failure`, and an optional JSON-excluded
cause. `errors.As` identifies the typed error; `errors.Is` can inspect its
wrapped cause, including `context.Canceled`. Invalid input, infrastructure failure
and cancellation return the partial report with a Go error. An ordinary test
expectation mismatch is represented by the report and does not produce a Go error.

## Manifest and validation boundary

[manifest-v1.json](../schema/manifest-v1.json) defines the JSON shape.
`schema_version` is exactly `1`. The required top-level members are `target`,
`variants.baseline`, `variants.candidate`, `test_inputs` and `tests`.

| Field | Contract |
| --- | --- |
| `target` | `native`, or `qemu-arm` with explicit executable and sysroot |
| Variant `cwd` | Directory below `Root` that neither equals nor contains the other variant's cwd; paths in that variant are relative to it |
| Variant `build` | Ordered argv commands with optional environment overrides and required timeout; an empty list permits existing artifacts |
| Variant `inputs` / `artifacts` | Nonempty declared source/config files and tested artifact paths |
| `test_inputs` | Shared declared files relative to `Root`; may be empty |
| Test `id` / `argv` | Unique ID and shared argv; executable must identify a declared artifact in both variants |
| Expectations | Exact normal exit code and optional literal stdout/stderr markers; all supplied markers must match |

Candidate expectations require exit `0`. A nonzero baseline expectation requires
at least one nonempty marker. Builds require exit `0`. The first argv element
must be nonempty; later empty arguments are valid. There is no implicit shell or
variable interpolation. An explicit shell in a trusted build command is allowed.

Limits are 1 MiB of manifest bytes, 256 tests, 64 build steps per variant,
1..3600 seconds per command and separate 1 MiB stdout/stderr capture limits.
Environment names follow `[A-Za-z_][A-Za-z0-9_]*`; values and argv cannot contain
NUL. Explicit command environment values override inherited values for that
child only.

The schemas reject unknown object properties and invalid structural values.
JSON Schema does not detect duplicate JSON keys after decoding, measure the
original byte length, inspect a filesystem, or establish all cross-field
relationships. The planner must also reject duplicate keys and test IDs,
overlapping variant directories (equal, or one containing the other), undeclared
executable artifacts, path escapes, symlink ancestors and declared files, missing
inputs/tools and invalid sysroots.
Both variants and all declared inputs are preflighted before the first command.
Artifacts may be absent before a build; post-build inspection must require
ordinary executable files with the supported ELF class, machine, endianness and
ABI. Supported hosts are Linux amd64/arm64; the emulated target is little-endian
ARM32 EABI5 hard-float. ELF field consistency is a planner responsibility.

## Identity and execution plan

`FileIdentity` records a path relative to `Root`, SHA-256 and byte size for one
declared file. `ArtifactIdentity` adds observed ELF fields. Input identities are
captured before builds; artifact identities are inspected immediately before and
after each variant's testing. Replaced paths, symlinks, missing files and changed
artifact identities are infrastructure failures. Digest lists are sorted by path.
These identities cover declared files, not a complete build environment or
trusted provenance.

`Load` retains original bytes in `Spec.ManifestJSON`, which is excluded from
JSON output. Before using those bytes for identity, `Prepare` must confirm that
they strictly decode to the current `Spec`. An unchanged loaded manifest uses
`ManifestIdentity.Encoding = "raw_json"`; a manually constructed or changed
`Spec` uses `"canonical_json"`, hashing its JSON representation without
`ManifestJSON`. Execution uses the validated current fields, never a stale raw
manifest. The identity encoding is explicit so callers can distinguish the two.

`Plan` and `VariantPlan` are per-invocation execution data, not report formats.
They contain the root, relative and resolved paths, ordered command requests,
target information and collected identities. Baseline precedes candidate;
commands/tests retain declaration order. `CommandRequest` carries the variant,
kind, ID, target, argv, absolute execution cwd, relative report cwd, environment
overrides and timeout. For a test, argv begins with the validated artifact path
relative to the variant cwd, with `./` where needed. `ExecutablePath` separately
resolves the executable for launch and is excluded from JSON. Native execution
uses that launch path while preserving relative `argv[0]`; QEMU execution adds
the explicit executable, `-L` and sysroot before the relative artifact argv. The
executor records actual argv verbatim without adding absolute workspace prefixes.
Build argv always runs directly on the host.

`TargetInfo` records the execution target, the host OS and architecture, and
the resolved absolute QEMU and sysroot paths that QEMU tests are launched
with. Observed QEMU argv must begin with exactly those strings.

`ToolIdentity` records a role, a path, and either a version observation or an
explicit `version_error`. A build tool path is the resolved executable that
is launched. It is relative to `Root`, with forward slashes, when it lies
inside `Root`; otherwise it is an absolute host path. The `qemu` and
`sysroot` observations repeat the `TargetInfo` paths.

Report cwd and identity paths never depend on Root's location. Tool and
target paths depend on it in two cases:

- A QEMU executable or sysroot lies inside `Root`. It keeps its absolute
  launch path.
- Path resolution crosses the `Root` boundary. This is a relative path that
  leaves `Root`, such as `../` argv, a relative PATH entry or a relative
  symlink. It is also a reference resolved from outside `Root` that lands
  inside it, such as an absolute argv, PATH entry or symlink target naming a
  location inside `Root`.

The reported path can then change when `Root` moves, and it always names the
location actually used. Manifest argv is recorded verbatim, and diagnostics,
program output and built artifacts can still contain absolute paths.

Arbitrary programs do not share a universal version flag. Unavailable version
evidence must not become a fabricated version or an environment-attestation
claim.

Build tools are therefore never run speculatively; each records this
`version_error`:

```text
not queried: build programs have no universal version option and are not run speculatively; record a version with an explicit build step such as ["gcc", "--version"], whose argv and captured output the report keeps
```

The sysroot records
`no universal sysroot version; directory environment is not attested`.

The emulator's version is observed. For a `qemu-arm` target, `Run` executes
the configured QEMU once with the single argument `--version`:

- It runs after preflight has fully succeeded, after the CLI has reserved its
  destination and before the first build.
- Besides the declared build and test commands, this is the only process
  TargetCheck starts. It never starts after an invalid manifest or a failed
  preflight.
- The first non-empty stdout line, trimmed, valid UTF-8 and at most 256 bytes
  long, becomes the `qemu` observation's `version`.
- A launch failure, nonzero exit, signal, timeout, output limit or missing or
  unusable line records a precise `version_error` instead, and the run
  continues: version evidence never fails a run.
- Cancellation during the query cancels the run as anywhere else, so every
  declared check remains `not_run` and the CLI exits 130.

[Command execution](execution.md#qemu-version-query) lists the exact texts. The
version is the emulator's own statement, not an attestation of its build.

## Component interfaces

These interfaces are declared in `internal/model`. Separately owned packages may
provide the same named functions; orchestration can adapt them to interfaces for
dependency injection and zero-launch preflight tests.

```go
type Planner interface {
    Load(io.Reader) (Spec, error)
    Prepare(Spec, Options) (Plan, error)
    InspectArtifacts(Plan, VariantPlan) ([]ArtifactIdentity, error)
}
type Executor interface {
    Execute(context.Context, CommandRequest) ProcessObservation
}
type Evaluator interface {
    Evaluate(Report) Report
    ExitCode(Report) int
}
type Renderer interface {
    WriteJSON(io.Writer, Report) error
    WriteMarkdown(io.Writer, Report) error
}
```

The planner does not launch commands or write inputs. The executor does not
evaluate expectations or write reports. The evaluator is pure and performs no
filesystem, process, network or formatting work. Renderers consume the evaluated
report, write only to the supplied writer, and neither execute commands nor
recalculate outcomes.

| Owner | Reserved implementation paths |
| --- | --- |
| Input planning, #3 | `internal/plan/`, `docs/input-validation.md` |
| Process execution, #4 | `internal/execute/`, `docs/execution.md` |
| Evaluation, #5 | `internal/evaluate/`, `docs/outcomes.md` |
| Rendering, #6 | `internal/render/`, `docs/report-format.md` |
| API/CLI orchestration, #7 | Public entry-point files, `cmd/targetcheck/`, `internal/cli/`, native integration tests |

Changes to shared model/schema contracts, Makefile and CI have one designated
owner and are coordinated before downstream callers change. A package must not
silently introduce a competing wire model.

## Observations, outcomes and reports

`ProcessObservation` records whether a program started, actual argv, relative cwd,
override names, duration, separate captures, and its normal exit or signal and
failure. A successfully launched program exiting `127` has `Started=true`, an
`ExitCode` pointer to `127` and no launch failure. Failure to launch has
`Started=false`, a `launch_failure`, and no normal exit code. Signal termination,
timeout, cancellation and output-limit causes remain explicit infrastructure
observations and cannot satisfy a negative test expectation.

The executor uses a separate process group. Timeout, cancellation and output
overflow stop the group, allow a bounded grace period, then kill and reap the
direct child. Completion is bounded by the configured timeout plus two seconds
for ordinary descendants. Programs deliberately escaping process groups are
outside this lifecycle guarantee; execution is not a sandbox.

Each `Capture` holds in-memory `Data` excluded from JSON, observed byte count,
truncation flag and optional safe relative log name. `CommandRequest.Env` and
`RunError.Cause` are also excluded from JSON. Reports contain only override names,
not their values or the inherited environment. Manifest `Command.Env` necessarily
contains input values, so a `Spec` or `Plan` must not be used as a report. Exact
argv, diagnostics and raw command output can contain sensitive data; this model
does not promise secret redaction.

`Report.Declared` records the expected coverage separately from received checks.
Orchestration initializes its two `VariantCoverage` entries directly from the
validated `Spec` before attaching observations. Entries are ordered baseline,
then candidate; `BuildIDs` are deterministic `build-0`, `build-1`, and so on, while
`TestIDs` preserve the exact declared test IDs and order shared by both variants.
Coverage must never be inferred from the checks that happen to remain in a report.

`Check` preserves ID, zero-based `Index` within its build/test list, kind, planned
argv, relative cwd, timeout, override names and expectation even before launch.
The pure shared helper `model.ValidateCoverage(Report) error` compares those checks
with `Declared`. It rejects missing variants or checks, duplicate IDs or indexes,
ID/index disagreement and empty test coverage. Thus even a trailing check omitted
from both variants remains detectable. The evaluator calls this helper before
evaluation, normalizes valid checks into declaration order, and cannot mark an
incomplete report matched. The helper validates coverage only; it does not assign
behavioral outcomes.

An absent observation is `not_run`; an empty collection is never success.
Baseline mismatches permit candidate execution; infrastructure/build failures
stop subsequent execution and preserve remaining checks as `not_run`.

| Check status | Meaning |
| --- | --- |
| `matched` | Normal exit and every supplied marker matched |
| `mismatch` | Normal behavior was observed but did not meet the expectation |
| `not_run` | No observation is available; the expectation was not established |
| `error` | Infrastructure or identity failure prevents a behavioral verdict |

Aggregate precedence is cancellation, infrastructure/invalid input, mismatch,
then matched. The pure exit mapping is:

| Aggregate outcome | CLI exit |
| --- | --- |
| `matched` | 0 |
| `mismatch` | 1 |
| `error` | 2 |
| `cancelled` | 130 |

SIGINT, SIGTERM and SIGHUP cancel (exit 130) unless the signal was ignored when
TargetCheck started, for example under `nohup`. A closing terminal sends
SIGHUP. Each such signal cancels the run, stops the running command's process
group and finalizes both reports before the CLI exits 130, so no command group
outlives the CLI.

A signal that was ignored at start stays ignored, and the run continues to its
normal result. `nohup` ignores SIGHUP, and non-interactive shells start
background jobs with SIGINT ignored. Go keeps such an inherited ignore only for
SIGINT and SIGHUP, so SIGTERM always cancels. The library installs no signal
handlers; its callers cancel the context.

Only after both reports were written completely does the CLI print its concise
stdout summary, `targetcheck: OUTCOME` followed by the JSON and Markdown report
paths, never raw logs. Exits 2 and 130 also print exactly one stderr diagnostic
line:

```text
targetcheck: error: KIND: MESSAGE
targetcheck: cancelled: MESSAGE
```

| Part | Value |
| --- | --- |
| `error` or `cancelled` | the report outcome |
| KIND | the report's `failure.kind`, omitted for cancellation because it would repeat the outcome |
| MESSAGE | the report's `failure.message` |

The line therefore agrees with the report and with the library's `RunError`.
Its `Error()` text is `CATEGORY: KIND: MESSAGE`, omitting KIND when it repeats
the category, as in
`invalid_input: missing_tool: QEMU executable is unavailable` and
`cancelled: run cancelled by caller`. Backslashes, control characters,
non-ASCII text and invalid UTF-8 in KIND and MESSAGE are written as Go escapes
(`\\`, `\n`, `\x1b`, `\u00e9`, `\xff`), so untrusted text cannot add lines or
terminal controls. For example:

```text
targetcheck: error: missing_tool: QEMU executable is unavailable
targetcheck: error: invalid_input: load manifest: invalid JSON manifest: duplicate key "schema_version"
targetcheck: error: build_failure: baseline build "build-1": exit 23; expected 0
targetcheck: error: timeout: baseline test "t2": command exceeded its configured timeout
targetcheck: cancelled: run cancelled by caller
```

When no report could be written, the line states the failure that the report
would have recorded and appends `; no report written: REASON`. If the
destination is refused before a valid manifest runs, REASON is itself the
failure, as in
`targetcheck: error: invalid_input: output directory already exists`. When
writing the reports fails, the line states the report's final failure, normally
`io: output persistence failed: ...`, and appends
`; incomplete output at "DIR"`. A cancelled run keeps the cancellation as its
failure and appends these notes. In these no-report and incomplete-output cases
only the stderr line appears, without a stdout summary. Command-line usage
errors print the same line with kind `invalid_input`, followed by the usage
text.

`Report` contains schema/tool version, target, manifest identity, declared input
identities, tool observations, declared coverage, ordered variant reports,
aggregate outcome and an optional failure. Variant reports preserve build/test
checks and artifact identities before/after testing. Early invalid-input reports
may lack identities or variants, have empty declared coverage, and must state
`error`. Empty declared coverage is not valid execution coverage and cannot
support a matched outcome. The report schema constrains structure and success
coverage; `ValidateCoverage` enforces cross-list coverage relationships. The
evaluator must also establish exact expectation matches, identity equality and
consistent aggregate precedence.

[report-v1.json](../schema/report-v1.json) is the authoritative JSON shape.
The fixture [catalog](../testdata/contracts/catalog.json) records valid and invalid
schema examples. JSON retains exact strings; Markdown escapes untrusted text and
uses safe relative log links. The CLI owns new-directory creation,
restricted-permission logs, atomic report writes and report-write errors. A
renderer error must propagate; a matching test suite does not turn a failed
report write into CLI success.

## Foundation verification environment

Go 1.27.4 was selected after checking the
[official download page](https://go.dev/dl/). The
[Containerfile](../dev/Containerfile) pins the official Linux image digest,
dated public Debian snapshot and compiler/QEMU package versions.
[tool-versions.txt](../dev/tool-versions.txt) records the selected tools.
The `go 1.27.4` module directive sets the required language/toolchain minimum;
Make and the container enforce the exact selected toolchain.

Prepare the environment with network access, then verify the committed snapshot
offline:

```sh
make container-build
make verify-container
```

The build passes only the Containerfile through stdin and sends no repository
context. `verify-container` transfers `git archive HEAD`, so local and untracked
files are excluded, and runs Docker with `--network=none`. It verifies the
committed snapshot, not uncommitted edits. Docker must already be available.
The container sets `GOTOOLCHAIN=local`, disables the module proxy/checksum service,
and includes the exact Go toolchain; no toolchain download is needed during
verification.

For the delivered packages, `make verify` checks Linux/toolchain prerequisites,
gofmt cleanliness, `go vet ./...`, `go test -race ./...` and `go build ./...`.
`make test-native` compiles real native C helpers and the CLI, then checks
API/CLI outcomes, partial reports, cancellation and filesystem boundaries.
`make verify-container` runs both commands against the committed snapshot with
networking disabled. There are no third-party Go dependencies. `make test-arm`,
`make demo`, `make dist` and `make test-container` cover ARM execution, the
real Wget scenarios, the release archives and the full offline suite; see the
[README](../README.md#development-and-acceptance). These native checks do not
claim ARM or Wget acceptance.
