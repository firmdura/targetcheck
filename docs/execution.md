# Command execution

`internal/execute.Execute(context.Context, model.CommandRequest)` implements the
frozen executor boundary. `execute.Executor{}` supplies the same operation through
`model.Executor`. Requests come from the planner; execution returns observations
without assigning expectation outcomes or writing reports.

In short, the executor guarantees that every command runs directly from its
argv in its own process group, that the group is stopped on a timeout,
cancellation or output overflow, that each output stream keeps at most 1 MiB,
and that a normal exit, including 1 or 127, is never confused with a launch
failure, signal or other infrastructure failure.

Execution requires Linux. Each command uses its own working directory and child
environment. Override names are sorted in the observation; values and the
inherited environment are never serialized. Raw stdout/stderr and exact argv can
contain sensitive information and are not redacted. The caller's cwd, environment
and request are unchanged, including during concurrent invocations.

Builds run directly on the host. Native tests use the resolved `ExecutablePath`
while preserving the planned relative argv. QEMU tests launch the explicitly
configured emulator with argv `QEMU -L SYSROOT ARTIFACT ARGS...`. No shell, download,
retry or installation is implicit. The QEMU argv test uses a recording program;
actual ARM execution is a separate mandatory acceptance step (`make test-arm`
and `make demo`).

Each started program has a separate process group. Caller cancellation,
including a caller context deadline, is `cancelled`; the command's configured
deadline is `timeout`. Either stop condition, or exceeding either output cap,
stops the group:

1. TERM is sent to the group.
2. The group gets 250 ms for cleanup.
3. KILL is sent, and the direct child is reaped.

Remaining ordinary descendants are also terminated when the direct child
exits. Each pipe is drained independently, with at most 500 ms of draining
after group cleanup. Completion after a configured timeout is bounded by that
timeout plus two seconds for ordinary processes. Deliberate process-group
escape and kernel uninterruptible states are outside this lifecycle guarantee.
This is not a sandbox, and the library does not alter process-global signal or
subreaper state.

The CLI cancels its context on SIGINT, SIGTERM and SIGHUP: these signals cancel
(exit 130) unless the signal was ignored when TargetCheck started, for example
under `nohup`. A closing terminal sends SIGHUP to the CLI or its shell, not to
the separate process groups of the commands, so this cancellation is what stops
them. A signal ignored at start stays ignored, and the run continues. Go keeps
such an inherited ignore only for SIGINT and SIGHUP, so SIGTERM always cancels.

Stdout and stderr each retain at most 1 MiB. Exactly 1 MiB is allowed; the first
excess byte marks truncation and triggers termination. `Capture.Bytes` counts
bytes actually read, including discarded excess; it does not estimate bytes a
terminated writer might otherwise have produced. Raw captures stay in memory,
excluded from JSON, until the CLI owns persistence.

A launched program returning 1 or 127 has its actual normal exit recorded and no
executor failure. A missing executable has `Started=false`, `launch_failure` and
no normal exit. Signals, timeout, cancellation, capture overflow and I/O failures
have explicit failure kinds and cannot masquerade as expected negative tests.
Cancellation takes precedence when it overlaps another outcome. The evaluator
later distinguishes an ordinary nonzero build exit from a test expectation.

## QEMU version query

Besides the declared build and test commands, the only process TargetCheck
starts is one version query for a `qemu-arm` target. Orchestration runs it after
preflight has fully succeeded, after the CLI has reserved its destination, and
before the first build; an invalid manifest or a failed preflight starts no
process at all. The query is an ordinary request to this executor and runs on
the host like a build:

| Property | Value |
| --- | --- |
| argv | `QEMU --version`, where QEMU is the resolved `target.executable` |
| Process group | its own |
| Working directory | Root |
| Environment | inherited, with no overrides |
| Timeout | a fixed 10 seconds |
| Capture limits | the standard limits |

The first non-empty stdout line, with surrounding whitespace trimmed,
becomes the `qemu` tool observation's `version` when it is valid UTF-8 and at
most 256 bytes long. Otherwise the observation records one of these
`version_error` values, and the run continues:

| Observation | `version_error` |
| --- | --- |
| Executor failure | `--version query failed: KIND: MESSAGE`, for example `--version query failed: launch_failure: cannot launch command (check executable, cwd and permissions): permission denied` |
| Timeout | `--version query failed: timeout: command exceeded its configured timeout (10 s)` |
| Signal | `--version query failed: signal: command terminated by signal SIGNAL` |
| Nonzero exit | `--version query failed: exit N; expected 0` |
| No non-empty stdout line | `--version query printed no non-empty stdout line` |
| Line too long | `--version query printed a first non-empty stdout line longer than 256 bytes` |
| Invalid UTF-8 | `--version query printed a first non-empty stdout line that is not valid UTF-8` |
| Run stopped before the query | `not queried: the run stopped before the --version query` |

Cancellation during the query records
`--version query failed: cancelled: command cancelled by caller` and cancels the
run as anywhere else: every declared check stays `not_run` and the CLI exits 130.
Version evidence never fails a run, and the version text is the emulator's own
statement, not an attestation of the emulator's build.

## Verification

Run the focused subprocess and race checks in the pinned Linux environment:

```sh
go test -race ./internal/execute/...
make verify
```

Tests compile the committed synthetic C helper with GCC and fail explicitly if
that prerequisite is missing. They exercise real processes, signals, concurrent
pipes, literal arguments, environment isolation, timeout/cancellation escalation,
descendant cleanup, direct-child reaping and repeated-call goroutine lifetime.
These helpers establish executor behavior, not Wget security or ARM target proof.
