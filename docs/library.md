# Go library

Use the package `github.com/firmdura/targetcheck/pkg/targetcheck` to run a
comparison from your own Go program. It has the same semantics as the
`targetcheck check` command, because both use the same engine and report
model. The package has no dependencies outside the Go standard library.

```sh
go get github.com/firmdura/targetcheck@v1.0.0
```

The module requires Go 1.27.4 or later. The reference documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/firmdura/targetcheck/pkg/targetcheck).
The public types are aliases of internal types, so `go doc` and the package's
pkg.go.dev page show them as `type Spec = model.Spec`, without their fields.
[Types](#types) below lists the fields.

`Load`, `WriteJSON` and `WriteMarkdown` only read from or write to the stream
you pass them, so they work on any platform. `Run` executes only on Linux
amd64 and arm64. Elsewhere it returns an `invalid_input` error without
starting anything.

## API

```text
func Load(r io.Reader) (Spec, error)
func Run(ctx context.Context, spec Spec, options Options) (Report, error)
func WriteJSON(w io.Writer, r Report) error
func WriteMarkdown(w io.Writer, r Report) error
```

- **`Load`** strictly decodes one schema-v1 manifest of at most 1 MiB, as the
  [manifest reference](usage.md#manifest-reference) describes. It keeps the
  original bytes in `Spec.ManifestJSON`. It starts no command and reads no
  file other than `r`. Every error it returns is a `*RunError` of kind
  `invalid_input`.
- **`Run`** validates the `Spec` again and runs preflight on both variants.
  Then it builds and tests the baseline and the candidate and evaluates every
  check, exactly like the CLI. It returns the evaluated report.
  - It never writes reports or logs, and it never downloads or installs
    anything.
  - It never changes the process's working directory, environment or signal
    handling, and it never calls `os.Exit`.
  - The commands it runs are trusted programs with your permissions; see
    [limitations.md](limitations.md#trusted-commands-not-a-sandbox).
  - Besides those commands, it starts one process. For a `qemu-arm` target,
    after preflight, it runs the configured emulator with the single argument
    `--version` to record its version
    ([execution.md](execution.md#qemu-version-query)).
- **`WriteJSON`** writes the authoritative schema-v1 JSON of one report to
  `w`, and **`WriteMarkdown`** writes its escaped Markdown rendering. Neither
  includes raw captured output. If the report is not a valid representation,
  both return an error before they write anything. They also return a failing
  writer's error.

`Options` has two fields:

| Field | Meaning |
| --- | --- |
| `Root` | required. The absolute input workspace. It and its ancestors must not be symlinks |
| `OutputDir` | optional. If set, `Run` also checks the destination the way the CLI does, and rejects it unless it is absolute and new, its parent exists without symlinks, and it is outside both variant directories. `Run` never creates or writes it |

You can also build a `Spec` in Go instead of loading one. `Run` validates it
the same way. It then records the manifest identity as the SHA-256 of the
spec's canonical JSON (`canonical_json`) instead of the original bytes
(`raw_json`).

## Example

[example_compare_test.go](../pkg/targetcheck/example_compare_test.go) writes a
workspace with the same small C component in `baseline/` and `candidate/`. The
candidate is built with `-DFIXED`. Then the example runs this code:

```go
	spec, err := targetcheck.Load(strings.NewReader(portManifest))
	if err != nil {
		panic(err) // a *targetcheck.RunError of kind invalid_input
	}
	report, err := targetcheck.Run(context.Background(), spec, targetcheck.Options{Root: root})
	var runErr *targetcheck.RunError
	if errors.As(err, &runErr) {
		// Invalid input, infrastructure failure or cancellation. The partial
		// report still records completed checks; the rest are not_run.
		fmt.Println("run failed:", runErr.Kind, runErr.Failure.Kind, report.Outcome)
		return
	}

	// A behavior mismatch is not a Go error: inspect the outcome and checks.
	// With a nil error, every check ran and exited normally.
	fmt.Println("outcome:", report.Outcome)
	for _, variant := range report.Variants {
		for _, test := range variant.Tests {
			fmt.Printf("%s %s: %s, exit %d\n", variant.Name, test.ID, test.Status, *test.Observation.ExitCode)
			fmt.Printf("  stdout: %q\n", test.Observation.Stdout.Data)
		}
	}

	// Run never writes files. Render the evidence wherever you need it.
	var jsonReport, markdownReport bytes.Buffer
	if err := targetcheck.WriteJSON(&jsonReport, report); err != nil {
		panic(err)
	}
	if err := targetcheck.WriteMarkdown(&markdownReport, report); err != nil {
		panic(err)
	}
	fmt.Println(strings.SplitN(markdownReport.String(), "\n", 2)[0])

	// The exit status the targetcheck CLI uses for this outcome.
	exitCode := 2 // error, and anything unexpected
	switch report.Outcome {
	case targetcheck.OutcomeMatched:
		exitCode = 0
	case targetcheck.OutcomeMismatch:
		exitCode = 1
	case targetcheck.OutcomeCancelled:
		exitCode = 130
	}
	fmt.Println("exit code:", exitCode)
```

which prints:

```text
outcome: matched
baseline reject-out-of-range: matched, exit 1
  stdout: "FAIL reject-out-of-range: accepted 70000\n"
candidate reject-out-of-range: matched, exit 0
  stdout: "PASS reject-out-of-range\n"
# TargetCheck report
exit code: 0
```

The baseline's expected failure matched, and the report still shows its
actual exit 1. On Linux, with `cc` and `sleep` on `PATH`,
`go test ./pkg/targetcheck/` compiles and runs this example and the package's
other examples.

## Errors

`Run` returns a nil error when every check ran and was evaluated. The outcome
is then `matched` or `mismatch`: a behavior mismatch is a result, not an
error.

Otherwise `Run` returns the partial report and a `*RunError`:

| `RunError.Kind` | When | `Report.Outcome` |
| --- | --- | --- |
| `invalid_input` | the manifest, `Options` or preflight is invalid, including a missing tool, QEMU executable or sysroot and an unsupported host; nothing ran | `error` |
| `infrastructure` | a build failed, or a command could not be launched, was killed by a signal, timed out, exceeded the output limit, or an artifact was invalid or changed; later checks are `not_run` | `error` |
| `cancelled` | the context was cancelled or reached its deadline | `cancelled` |

To inspect the error:

- `errors.As(err, &runErr)` finds the typed error.
- `RunError.Failure` holds the specific failure kind and message, the same
  values as `Report.Failure`. For a failed command, the message names the
  check, as in `baseline build "build-0": exit 1; expected 0`.
- `RunError.Error()` returns `CATEGORY: KIND: MESSAGE`, and leaves out `KIND`
  when it equals the category. Examples are
  `invalid_input: missing_tool: QEMU executable is unavailable` and
  `cancelled: run cancelled by caller`.
- `errors.Is` sees the cause through `Unwrap`, for example `context.Canceled`
  or `context.DeadlineExceeded`.

What the partial report contains depends on how far the run got:

- Once preflight has succeeded, the report keeps the declared coverage and
  the results of every completed check. Checks that did not complete are
  `not_run` or `error`, never `matched`.
- A report from a failed manifest or preflight has the failure, but no target
  kind, manifest digest, coverage or variants.

The precedence is the same as the CLI's: cancellation, then infrastructure
and invalid input, then mismatch, then matched.

## Cancellation and deadlines

Pass a context to limit or cancel a run. When the context is cancelled or
passes its deadline, `Run` stops the running command's process group (TERM,
then KILL after a grace period) and returns a `cancelled` report.
[example_deadline_test.go](../pkg/targetcheck/example_deadline_test.go) runs
builds that sleep for 60 seconds, with a 500 ms deadline:

```go
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	report, err := targetcheck.Run(ctx, spec, targetcheck.Options{Root: root})

	var runErr *targetcheck.RunError
	if errors.As(err, &runErr) && runErr.Kind == targetcheck.ErrorCancelled {
		fmt.Println("cancelled:", runErr.Failure.Message)
	}
	fmt.Println("error:", err)
	fmt.Println("deadline exceeded:", errors.Is(err, context.DeadlineExceeded))
	fmt.Println("outcome:", report.Outcome)
	for _, variant := range report.Variants {
		for _, checks := range [][]targetcheck.Check{variant.Build, variant.Tests} {
			for _, check := range checks {
				fmt.Printf("%s %s %s: %s\n", variant.Name, check.Kind, check.ID, check.Status)
			}
		}
	}
```

```text
cancelled: run cancelled by caller
error: cancelled: run cancelled by caller
deadline exceeded: true
outcome: cancelled
baseline build build-0: error
baseline test smoke: not_run
candidate build build-0: not_run
candidate test smoke: not_run
```

A caller deadline counts as cancellation. A command that exceeds its own
`timeout_seconds` is a `timeout` infrastructure failure instead.

`Run` does not install signal handlers. The CLI cancels on SIGINT, SIGTERM and
SIGHUP, except for a signal that was already ignored when it started
([usage.md](usage.md#exit-codes) has the rules). Your program can do the same:

1. Pass to `signal.NotifyContext` only the signals for which `signal.Ignored`
   reports false.
2. If no signals remain, do not call `signal.NotifyContext` at all, because a
   call without signals relays every signal.

The CLI's own [main.go](../cmd/targetcheck/main.go) does exactly this.

## Concurrency and state

All run state is per call. Concurrent `Run` calls on separate roots are
independent, and none of them changes the caller's working directory or
environment. Commands inherit the process environment plus each command's
declared overrides. `Run` keeps up to 1 MiB per stream of every command's
output in memory, in `Capture.Data`, until you discard the report.

## Mapping a report to an exit status

The example above maps `Report.Outcome` to the CLI's exit codes: `matched` 0,
`mismatch` 1, `error` 2 and `cancelled` 130. For the reports that `Run`
returns, this equals the CLI's status. The one difference is that the CLI also
exits 2 when it cannot write the reports.

## Version information

`Report.Tool` records `Version`, `Commit` and `GoVersion`. When your program
depends on the module:

- `Version` is the module version from your binary's build information, such
  as `v1.0.0`;
- `Commit` is `unknown`;
- `GoVersion` is the Go version that built your program.

## Types

All public types are aliases of `internal/model` types. The JSON column uses
the names from [report-v1.json](../schema/report-v1.json) and
[manifest-v1.json](../schema/manifest-v1.json).

### Manifest types

| Type | Field | JSON | Meaning |
| --- | --- | --- | --- |
| `Spec` | `SchemaVersion int` | `schema_version` | must be `SchemaVersion` (1) |
| | `Target Target` | `target` | where tests run |
| | `Variants Variants` | `variants` | the baseline and the candidate |
| | `TestInputs []string` | `test_inputs` | shared declared files relative to `Root` |
| | `Tests []Test` | `tests` | the shared ordered tests |
| | `ManifestJSON []byte` | not serialized | the original bytes kept by `Load`, used for the `raw_json` identity while the `Spec` is unchanged |
| `Target` | `Kind TargetKind` | `kind` | `TargetNative` or `TargetQEMUARM` |
| | `Executable string` | `executable` | QEMU executable, `qemu-arm` only |
| | `Sysroot string` | `sysroot` | QEMU sysroot, `qemu-arm` only |
| `Variants` | `Baseline Variant` | `baseline` | |
| | `Candidate Variant` | `candidate` | |
| `Variant` | `CWD string` | `cwd` | variant directory relative to `Root` |
| | `Build []Command` | `build` | ordered build steps |
| | `Inputs []string` | `inputs` | declared source and configuration files, relative to `CWD` |
| | `Artifacts []string` | `artifacts` | tested executables relative to `CWD` |
| `Command` | `Argv []string` | `argv` | program and literal arguments |
| | `Env map[string]string` | `env` | environment overrides for this command |
| | `TimeoutSeconds int` | `timeout_seconds` | 1 to 3600 |
| `Test` | `ID string` | `id` | unique name |
| | `Argv []string` | `argv` | declared artifact and literal arguments |
| | `TimeoutSeconds int` | `timeout_seconds` | 1 to 3600 |
| | `BaselineExpect Expectation` | `baseline_expect` | |
| | `CandidateExpect Expectation` | `candidate_expect` | exit code must be 0 |
| `Expectation` | `ExitCode int` | `exit_code` | exact normal exit, 0 to 255 |
| | `StdoutContains string` | `stdout_contains` | literal substring, optional |
| | `StderrContains string` | `stderr_contains` | literal substring, optional |
| `Options` | `Root string` | | absolute input workspace |
| | `OutputDir string` | | optional destination to validate, never written |

A `Spec` holds the values of environment overrides, so never publish a `Spec`
as a report.

### Report types

| Type | Field | JSON | Meaning |
| --- | --- | --- | --- |
| `Report` | `SchemaVersion int` | `schema_version` | 1 |
| | `Tool ToolVersion` | `tool` | TargetCheck version information |
| | `Target TargetInfo` | `target` | target and host |
| | `Manifest *ManifestIdentity` | `manifest` | nil when the manifest or preflight failed |
| | `Inputs []FileIdentity` | `inputs` | declared inputs and test inputs, sorted by path |
| | `Tools []ToolIdentity` | `tools` | tool paths and versions, sorted by role |
| | `Declared []VariantCoverage` | `declared` | coverage declared by the manifest; empty when the manifest or preflight failed |
| | `Variants []VariantReport` | `variants` | baseline, then candidate; empty when preflight failed |
| | `Outcome Outcome` | `outcome` | aggregate result |
| | `Failure *Failure` | `failure` | set for `error` and `cancelled` |
| `ToolVersion` | `Version`, `Commit`, `GoVersion string` | `version`, `commit`, `go_version` | |
| `TargetInfo` | `Kind TargetKind` | `kind` | empty when the manifest or preflight failed |
| | `HostOS`, `HostArch string` | `host_os`, `host_arch` | for example `linux`, `arm64` |
| | `Executable`, `Sysroot string` | `executable`, `sysroot` | resolved absolute QEMU paths that tests were launched with |
| `ManifestIdentity` | `SHA256 string` | `sha256` | |
| | `Encoding string` | `encoding` | `raw_json` or `canonical_json` |
| `FileIdentity` | `Path string` | `path` | relative to `Root` |
| | `SHA256 string` | `sha256` | lowercase hex |
| | `SizeBytes int64` | `size_bytes` | |
| `ArtifactIdentity` | `FileIdentity` (embedded) | `path`, `sha256`, `size_bytes` | |
| | `ELF ELFIdentity` | `elf` | |
| `ELFIdentity` | `Class`, `Machine`, `Endian`, `ABI string` | `class`, `machine`, `endian`, `abi` | for example `ELF32`, `EM_ARM`, `little`, `arm-eabi5-hard-float` |
| `ToolIdentity` | `Role string` | `role` | `baseline/build-0`, `qemu`, `sysroot`, ... |
| | `Path string` | `path` | resolved path; a build tool inside `Root` is relative to it |
| | `Version`, `VersionError string` | `version`, `version_error` | exactly one is set; only `qemu` can have a `version` |
| `VariantCoverage` | `Name VariantName` | `name` | |
| | `BuildIDs`, `TestIDs []string` | `build_ids`, `test_ids` | `build-0`, `build-1`, ... and the manifest test IDs |
| `VariantReport` | `Name VariantName` | `name` | `Baseline` or `Candidate` |
| | `CWD string` | `cwd` | relative to `Root` |
| | `Build`, `Tests []Check` | `build`, `tests` | in declaration order |
| | `ArtifactsBefore`, `ArtifactsAfter []ArtifactIdentity` | `artifacts_before`, `artifacts_after` | immediately before and after the tests |
| `Check` | `ID string`, `Index int` | `id`, `index` | test ID or `build-N`, and zero-based position |
| | `Kind CheckKind` | `kind` | `CheckBuild` or `CheckTest` |
| | `Argv []string`, `CWD string` | `argv`, `cwd` | planned invocation |
| | `EnvNames []string` | `env_names` | sorted override names |
| | `TimeoutSeconds int` | `timeout_seconds` | |
| | `Expectation Expectation` | `expectation` | exit 0 for builds |
| | `Observation *ProcessObservation` | `observation` | nil when the check did not run |
| | `Status CheckStatus` | `status` | `matched`, `mismatch`, `not_run` or `error` |
| | `Reason string` | `reason` | why |
| `ProcessObservation` | `Started bool` | `started` | false when the program could not be launched |
| | `Argv []string`, `CWD string` | `argv`, `cwd` | actual invocation, including any QEMU prefix |
| | `EnvNames []string` | `env_names` | |
| | `ExitCode *int` | `exit_code` | nil unless the program exited normally |
| | `Signal string` | `signal` | terminating signal, if any |
| | `Failure *Failure` | `failure` | launch, signal, timeout, cancellation, output-limit or I/O failure |
| | `Stdout`, `Stderr Capture` | `stdout`, `stderr` | |
| | `DurationMillis int64` | `duration_millis` | |
| `Capture` | `Data []byte` | not serialized | the captured bytes, at most 1 MiB |
| | `Bytes int64` | `bytes` | bytes observed, including any discarded excess |
| | `Truncated bool` | `truncated` | the limit was exceeded |
| | `Log string` | `log` | log file name; set by the CLI, empty from `Run` |
| `Failure` | `Kind FailureKind`, `Message string` | `kind`, `message` | |
| `RunError` | `Kind ErrorKind` | `kind` | |
| | `Failure Failure` | `failure` | |
| | `Cause error` | not serialized | returned by `Unwrap` |

If you persist logs yourself, name them the way the renderers expect. They
accept a `Capture.Log` only of the form `logs/NAME.stdout` or
`logs/NAME.stderr`, where `NAME` is made of letters, digits, `_` and `-`.

### Constants

| Type | Constants |
| --- | --- |
| untyped | `SchemaVersion` = 1 |
| `TargetKind` | `TargetNative` (`native`), `TargetQEMUARM` (`qemu-arm`) |
| `VariantName` | `Baseline` (`baseline`), `Candidate` (`candidate`) |
| `CheckKind` | `CheckBuild` (`build`), `CheckTest` (`test`) |
| `CheckStatus` | `CheckMatched`, `CheckMismatch`, `CheckNotRun`, `CheckError` (`matched`, `mismatch`, `not_run`, `error`) |
| `Outcome` | `OutcomeMatched`, `OutcomeMismatch`, `OutcomeError`, `OutcomeCancelled` (`matched`, `mismatch`, `error`, `cancelled`) |
| `ErrorKind` | `ErrorInvalidInput`, `ErrorInfrastructure`, `ErrorCancelled` (`invalid_input`, `infrastructure`, `cancelled`) |
| `FailureKind` | `FailureInvalidInput` (`invalid_input`), `FailureMissingTool` (`missing_tool`), `FailureBuild` (`build_failure`), `FailureLaunch` (`launch_failure`), `FailureSignal` (`signal`), `FailureTimeout` (`timeout`), `FailureCancelled` (`cancelled`), `FailureOutputLimit` (`output_limit`), `FailureIdentity` (`identity_change`), `FailureArtifact` (`invalid_artifact`), `FailureIO` (`io`), `FailureNotRun` (`not_run`) |

The full contract, including identity encodings and coverage validation, is in
[contracts.md](contracts.md).
