# Evaluating observed outcomes

The evaluator compares recorded observations with declared expectations. It
performs no filesystem, process, network or report-writing work. In short, a
test matches only with its exact normal exit and every supplied literal
marker, infrastructure failures never count as test behavior, missing
evidence never produces a matched result, and the aggregate outcome maps to
one exit code. Passing the declared tests establishes those expectations for
the identified artifacts; it does not establish CVE remediation or general
security.

`internal/evaluate.Evaluate(model.Report) model.Report` returns an evaluated copy.
The stateless `evaluate.Evaluator` also implements the shared interface. The
function recomputes check statuses and the aggregate outcome rather than trusting
incoming success labels. It preserves explicit report failures supplied by
orchestration, such as a failed artifact revalidation or cancellation outside a
running child. Metadata is copied before normalization; raw capture bytes remain
shared and are read only. The caller's report and observations are not modified.

The report's independent `Declared` coverage must come from the validated
manifest before any execution. The evaluator calls `model.ValidateCoverage`;
missing variants, omitted checks, duplicate IDs/indexes, or inconsistent ordering
cannot produce a matched result. This includes a final test omitted from both
variants. Output order is baseline then candidate and declaration index within
each build/test list. Input and artifact identities are sorted by path.

Every test requires its exact normal exit and every supplied literal
`stdout_contains` and `stderr_contains` marker. Markers are byte substrings, not
regular expressions. A nonzero baseline expectation needs at least one nonempty
marker; a candidate expectation must require exit zero. An observed baseline exit
127 can satisfy an explicit exit-127 expectation and marker. A failure to start
the program cannot satisfy that expectation, even if a contradictory observation
also contains an exit value or matching text. Builds require only normal exit
zero; an unsuccessful build is infrastructure failure.

Signals, launch errors, timeouts, cancellation, output-limit failures, incomplete
captures and changed artifact identities are infrastructure observations, not
ordinary test behavior. A signal accompanying an exit value is never a normal
exit. Capture truncation or a byte count exceeding the limit fails regardless of
coincidental output markers. Missing capture bytes are an evidence gap; evaluating
a JSON report after raw bytes have been removed cannot recreate its verdict.
Render the already evaluated report instead of re-evaluating metadata-only JSON.

| Check status | Meaning |
| --- | --- |
| `matched` | Exact normal exit and all required markers matched |
| `mismatch` | Observed normal test behavior failed an expectation |
| `not_run` | No process observation was supplied |
| `error` | Infrastructure or artifact identity prevents a behavioral verdict |

The actual nonzero exit and baseline expectation remain in a matched negative
test; the evaluator does not call the program successful. A baseline mismatch
still permits evaluation of available candidate observations. Completed checks
remain present; unavailable observations remain `not_run`. Empty evidence never
produces a matched outcome. Required manifest, input and artifact identities must
be present and consistent. Artifact metadata and digest comparisons consume the
planner's observations; the evaluator does not inspect the current files.

Normal observations must identify the planned invocation: the same variant cwd
and override names, exact host build argv, or the declared relative test artifact
and arguments. QEMU observations additionally require the configured executable,
`-L` and sysroot prefix. Lexically equivalent relative `./` artifact paths are
accepted. Before/after identities must cover each launched test's artifact path;
unchanged hashes of an unrelated file cannot establish the test's identity.

Aggregate precedence and the pure `ExitCode` mapping are:

| Outcome | CLI exit | Meaning |
| --- | --- | --- |
| `cancelled` | 130 | Cancellation takes precedence over earlier errors/mismatches |
| `error` | 2 | Invalid, missing or infrastructure evidence prevents completion |
| `mismatch` | 1 | At least one test expectation mismatched with complete evidence |
| `matched` | 0 | Every declared expectation matched with complete evidence |

SIGINT, SIGTERM and SIGHUP cancel (exit 130) unless the signal was ignored when
TargetCheck started, for example under `nohup`. Go keeps such an inherited
ignore only for SIGINT and SIGHUP, so SIGTERM always cancels.

The aggregate `failure.message`, which the CLI also prints, names the check that
ended the run as `VARIANT KIND "ID": DETAIL`:

| Failure | Example `failure.message` |
| --- | --- |
| Build exit | `baseline build "build-1": exit 23; expected 0` |
| Executor failure | `baseline test "t2": command exceeded its configured timeout` |

For an executor failure (launch failure, signal, timeout, output limit, I/O or
cancellation of a running command), DETAIL is the executor's message. The
check's own `reason` stays `KIND: DETAIL`, such as
`timeout: command exceeded its configured timeout`.

The first failing check in baseline-then-candidate declaration order provides
the aggregate. A report-level failure recorded by orchestration outranks check
failures, and cancellation outranks everything: a run cancelled by its caller
reads `run cancelled by caller`.

A failure found when a variant's artifacts are inspected again after testing
voids the verdict of every test that ran in that variant, so orchestration
names those tests: `baseline test "t1": ...` for one test, or
`baseline tests "t1" to "t3": ...` from the first to the last test that ran. A
changed identity reads `artifact identity changed during testing: PATH`, or
`PATH and N more` when several artifacts changed. An inspection error keeps the
planner's message, such as
`artifact baseline/runner: no such file or directory`.

Call `ExitCode` on an evaluated report. It fails closed for contradictory or
incomplete aggregate/check states. The API, CLI and renderers consume the same
authoritative outcome and reasons. A report-write failure remains the CLI's
responsibility and changes its final exit to 2 even when evaluation matched.

Focused verification is `go test -race ./internal/evaluate/...` in the pinned
Linux environment. Tests use explicitly synthetic observations and do not claim
native process, QEMU, Wget or release acceptance.
