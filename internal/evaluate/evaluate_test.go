// SPDX-License-Identifier: Apache-2.0

package evaluate_test

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"testing/quick"

	"github.com/firmdura/targetcheck/internal/evaluate"
	"github.com/firmdura/targetcheck/internal/model"
)

// These reports are synthetic observations for a pure evaluator. They are not
// subprocess, target execution, Wget, or release acceptance evidence.
func observedReport() model.Report {
	r := model.Report{
		SchemaVersion: model.SchemaVersion,
		Tool:          model.ToolVersion{Version: "0.1.0-test", Commit: "synthetic", GoVersion: "go1.27.4"},
		Target:        model.TargetInfo{Kind: model.TargetNative, HostOS: "linux", HostArch: "amd64"},
		Manifest:      &model.ManifestIdentity{SHA256: strings.Repeat("0", 64), Encoding: "raw_json"},
		Outcome:       model.OutcomeMatched,
	}
	for _, name := range []model.VariantName{model.Baseline, model.Candidate} {
		cwd := string(name)
		r.Inputs = append(r.Inputs, model.FileIdentity{Path: cwd + "/source.c", SHA256: strings.Repeat("1", 64), SizeBytes: 42})
		r.Declared = append(r.Declared, model.VariantCoverage{Name: name, BuildIDs: []string{"build-0"}, TestIDs: []string{"boundary"}})
		build := model.Check{ID: "build-0", Index: 0, Kind: model.CheckBuild, Argv: []string{"cc", "source.c", "-o", "component"}, CWD: cwd, TimeoutSeconds: 30, Expectation: model.Expectation{ExitCode: 0}, Observation: observation(0, "", ""), Status: model.CheckMatched}
		build.Observation.Argv, build.Observation.CWD = append([]string(nil), build.Argv...), cwd
		test := model.Check{ID: "boundary", Index: 0, Kind: model.CheckTest, Argv: []string{"component", "boundary"}, CWD: cwd, TimeoutSeconds: 5, Expectation: model.Expectation{ExitCode: 0}, Observation: observation(0, "candidate done\n", ""), Status: model.CheckMatched}
		if name == model.Baseline {
			test.Expectation = model.Expectation{ExitCode: 1, StderrContains: "boundary assertion"}
			test.Observation = observation(1, "", "boundary assertion\n")
		}
		test.Observation.Argv, test.Observation.CWD = []string{"./component", "boundary"}, cwd
		artifact := model.ArtifactIdentity{FileIdentity: model.FileIdentity{Path: cwd + "/component", SHA256: strings.Repeat("2", 64), SizeBytes: 100}, ELF: model.ELFIdentity{Class: "ELF64", Machine: "EM_X86_64", Endian: "little", ABI: "linux-amd64"}}
		r.Variants = append(r.Variants, model.VariantReport{Name: name, CWD: cwd, Build: []model.Check{build}, Tests: []model.Check{test}, ArtifactsBefore: []model.ArtifactIdentity{artifact}, ArtifactsAfter: []model.ArtifactIdentity{artifact}})
	}
	return r
}

func observation(exit int, stdout, stderr string) *model.ProcessObservation {
	return &model.ProcessObservation{Started: true, ExitCode: &exit, Stdout: model.Capture{Data: []byte(stdout), Bytes: int64(len(stdout))}, Stderr: model.Capture{Data: []byte(stderr), Bytes: int64(len(stderr))}, DurationMillis: 1}
}

func TestBehaviorAndInfrastructureMatrix(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*model.Report)
		outcome model.Outcome
		code    int
		failure model.FailureKind
	}{
		{"expected negative baseline and successful candidate", func(*model.Report) {}, model.OutcomeMatched, 0, ""},
		{"baseline marker missing", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Stderr = model.Capture{} }, model.OutcomeMismatch, 1, ""},
		{"unexpected baseline success", func(r *model.Report) { *r.Variants[0].Tests[0].Observation.ExitCode = 0 }, model.OutcomeMismatch, 1, ""},
		{"different exact baseline exit", func(r *model.Report) { *r.Variants[0].Tests[0].Observation.ExitCode = 2 }, model.OutcomeMismatch, 1, ""},
		{"candidate failure", func(r *model.Report) { *r.Variants[1].Tests[0].Observation.ExitCode = 1 }, model.OutcomeMismatch, 1, ""},
		{"candidate observation missing", func(r *model.Report) { r.Variants[1].Tests[0].Observation = nil }, model.OutcomeError, 2, model.FailureNotRun},
		{"candidate omitted", func(r *model.Report) { r.Variants = r.Variants[:1] }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"signal with coincidental marker and exit", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Signal = "SIGSEGV" }, model.OutcomeError, 2, model.FailureSignal},
		{"unstarted process with coincidental exit and marker", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Started = false }, model.OutcomeError, 2, model.FailureLaunch},
		{"launch failure", func(r *model.Report) {
			r.Variants[0].Tests[0].Observation.Failure = &model.Failure{Kind: model.FailureLaunch, Message: "missing executable"}
		}, model.OutcomeError, 2, model.FailureLaunch},
		{"timeout", func(r *model.Report) {
			r.Variants[0].Tests[0].Observation.Failure = &model.Failure{Kind: model.FailureTimeout, Message: "command timed out"}
		}, model.OutcomeError, 2, model.FailureTimeout},
		{"explicit output limit", func(r *model.Report) {
			r.Variants[0].Tests[0].Observation.Failure = &model.Failure{Kind: model.FailureOutputLimit, Message: "stdout limit"}
		}, model.OutcomeError, 2, model.FailureOutputLimit},
		{"truncated capture cannot match", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Stderr.Truncated = true }, model.OutcomeError, 2, model.FailureOutputLimit},
		{"oversized capture metadata", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Stderr.Bytes = model.MaxCaptureBytes + 1 }, model.OutcomeError, 2, model.FailureOutputLimit},
		{"missing raw capture", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Stderr.Data = nil }, model.OutcomeError, 2, model.FailureIO},
		{"build failure", func(r *model.Report) { *r.Variants[0].Build[0].Observation.ExitCode = 1 }, model.OutcomeError, 2, model.FailureBuild},
		{"artifact changed", func(r *model.Report) { r.Variants[1].ArtifactsAfter[0].SHA256 = strings.Repeat("3", 64) }, model.OutcomeError, 2, model.FailureIdentity},
		{"artifact final identity missing", func(r *model.Report) { r.Variants[1].ArtifactsAfter = nil }, model.OutcomeError, 2, model.FailureArtifact},
		{"wrong artifact ABI", func(r *model.Report) { r.Variants[1].ArtifactsBefore[0].ELF.ABI = "arm-eabi5-hard-float" }, model.OutcomeError, 2, model.FailureArtifact},
		{"explicit identity error", func(r *model.Report) {
			r.Failure = &model.Failure{Kind: model.FailureIdentity, Message: "path replaced by symlink"}
		}, model.OutcomeError, 2, model.FailureIdentity},
		{"cancellation after mismatch", func(r *model.Report) {
			*r.Variants[0].Tests[0].Observation.ExitCode = 0
			r.Variants[1].Tests[0].Observation.Failure = &model.Failure{Kind: model.FailureCancelled, Message: "cancelled"}
		}, model.OutcomeCancelled, 130, model.FailureCancelled},
		{"cancellation outranks previous infrastructure", func(r *model.Report) {
			r.Failure = &model.Failure{Kind: model.FailureIO, Message: "earlier I/O"}
			r.Variants[1].Tests[0].Observation.Failure = &model.Failure{Kind: model.FailureCancelled, Message: "cancelled"}
		}, model.OutcomeCancelled, 130, model.FailureCancelled},
		{"early cancellation", func(r *model.Report) {
			r.Failure = &model.Failure{Kind: model.FailureCancelled, Message: "cancelled before launch"}
			for i := range r.Variants {
				r.Variants[i].Build[0].Observation = nil
				r.Variants[i].Tests[0].Observation = nil
			}
		}, model.OutcomeCancelled, 130, model.FailureCancelled},
		{"baseline negative expectation lacks marker", func(r *model.Report) { r.Variants[0].Tests[0].Expectation.StderrContains = "" }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"illegal candidate expectation", func(r *model.Report) { r.Variants[1].Tests[0].Expectation.ExitCode = 1 }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"missing normal exit", func(r *model.Report) { r.Variants[0].Tests[0].Observation.ExitCode = nil }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"negative exit is not normal", func(r *model.Report) { *r.Variants[0].Tests[0].Observation.ExitCode = -1 }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"missing manifest identity", func(r *model.Report) { r.Manifest = nil }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"missing input identities", func(r *model.Report) { r.Inputs = nil }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"ordinary exit127 can match", func(r *model.Report) {
			r.Variants[0].Tests[0].Expectation.ExitCode = 127
			*r.Variants[0].Tests[0].Observation.ExitCode = 127
		}, model.OutcomeMatched, 0, ""},
		{"launch failure cannot masquerade as exit127", func(r *model.Report) {
			r.Variants[0].Tests[0].Expectation.ExitCode = 127
			*r.Variants[0].Tests[0].Observation.ExitCode = 127
			r.Variants[0].Tests[0].Observation.Started = false
		}, model.OutcomeError, 2, model.FailureLaunch},
		{"stale labels cannot force success", func(r *model.Report) {
			r.Outcome = model.OutcomeMatched
			r.Variants[0].Tests[0].Status = model.CheckMatched
			*r.Variants[0].Tests[0].Observation.ExitCode = 0
		}, model.OutcomeMismatch, 1, ""},
		{"wrong observed cwd", func(r *model.Report) { r.Variants[1].Tests[0].Observation.CWD = "baseline" }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"wrong planned cwd", func(r *model.Report) {
			r.Variants[1].Tests[0].CWD = "other"
			r.Variants[1].Tests[0].Observation.CWD = "other"
		}, model.OutcomeError, 2, model.FailureInvalidInput},
		{"wrong test executable", func(r *model.Report) { r.Variants[1].Tests[0].Observation.Argv[0] = "./other" }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"wrong test argument", func(r *model.Report) { r.Variants[1].Tests[0].Observation.Argv[1] = "unrelated" }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"wrong build argv", func(r *model.Report) { r.Variants[1].Build[0].Observation.Argv[0] = "other-cc" }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"wrong override names", func(r *model.Report) { r.Variants[1].Build[0].Observation.EnvNames = []string{"UNDECLARED"} }, model.OutcomeError, 2, model.FailureInvalidInput},
		{"unrelated unchanged artifact", func(r *model.Report) {
			r.Variants[1].ArtifactsBefore[0].Path = "candidate/unrelated"
			r.Variants[1].ArtifactsAfter[0].Path = "candidate/unrelated"
		}, model.OutcomeError, 2, model.FailureArtifact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := observedReport()
			tc.change(&r)
			before := encodedInput(t, r)
			got := evaluate.Evaluate(r)
			if got.Outcome != tc.outcome || evaluate.ExitCode(got) != tc.code {
				t.Fatalf("outcome=%s exit=%d, want %s/%d failure=%+v", got.Outcome, evaluate.ExitCode(got), tc.outcome, tc.code, got.Failure)
			}
			if tc.failure == "" {
				if got.Failure != nil {
					t.Fatalf("unexpected failure %+v", got.Failure)
				}
			} else if got.Failure == nil || got.Failure.Kind != tc.failure {
				t.Fatalf("failure=%+v, want %s", got.Failure, tc.failure)
			}
			if !bytes.Equal(before, encodedInput(t, r)) {
				t.Fatal("evaluation mutated input including raw observations")
			}
			for _, v := range got.Variants {
				for _, c := range append(append([]model.Check(nil), v.Build...), v.Tests...) {
					if c.Reason == "" {
						t.Fatalf("missing explanation for %s", c.ID)
					}
					if c.Observation == nil && c.Status != model.CheckNotRun {
						t.Fatal("missing observation is not not_run")
					}
				}
			}
		})
	}
}

func TestAllMarkersAreLiteralAndRequired(t *testing.T) {
	r := observedReport()
	c := &r.Variants[0].Tests[0]
	c.Expectation.StdoutContains = "a.b"
	c.Expectation.StderrContains = "[boundary]"
	setOutput(c.Observation, "axb", "[boundary]")
	got := evaluate.Evaluate(r)
	if got.Outcome != model.OutcomeMismatch || !strings.Contains(got.Variants[0].Tests[0].Reason, "stdout missing literal marker") {
		t.Fatalf("nonliteral match accepted: %+v", got)
	}
	setOutput(c.Observation, "a.b", "[boundary]")
	if got = evaluate.Evaluate(r); got.Outcome != model.OutcomeMatched {
		t.Fatalf("both literal markers should match: %+v", got.Failure)
	}
	setOutput(c.Observation, "a.b", "boundary")
	if got = evaluate.Evaluate(r); got.Outcome != model.OutcomeMismatch {
		t.Fatal("missing second marker was accepted")
	}
}

// The aggregate failure, which the CLI prints, names the variant, kind and ID
// of the check that ended in an executor or infrastructure failure, while the
// check's own reason keeps the failure kind and the executor's detail.
func TestAggregateFailureNamesTheCheck(t *testing.T) {
	executorFailure := func(variant, list int, kind model.FailureKind, message string) func(*model.Report) {
		return func(r *model.Report) {
			checks := r.Variants[variant].Tests
			if list == 0 {
				checks = r.Variants[variant].Build
			}
			checks[0].Observation.ExitCode = nil
			checks[0].Observation.Failure = &model.Failure{Kind: kind, Message: message}
		}
	}
	cases := []struct {
		name    string
		change  func(*model.Report)
		outcome model.Outcome
		kind    model.FailureKind
		message string
		reason  string
	}{
		{"test timeout", executorFailure(0, 1, model.FailureTimeout, "command exceeded its configured timeout"), model.OutcomeError, model.FailureTimeout,
			`baseline test "boundary": command exceeded its configured timeout`, "timeout: command exceeded its configured timeout"},
		{"build launch failure", executorFailure(1, 0, model.FailureLaunch, "cannot launch command (check executable, cwd and permissions): permission denied"), model.OutcomeError, model.FailureLaunch,
			`candidate build "build-0": cannot launch command (check executable, cwd and permissions): permission denied`, "launch_failure: cannot launch command (check executable, cwd and permissions): permission denied"},
		{"test signal", executorFailure(1, 1, model.FailureSignal, "command terminated by signal killed"), model.OutcomeError, model.FailureSignal,
			`candidate test "boundary": command terminated by signal killed`, "signal: command terminated by signal killed"},
		{"test output limit", executorFailure(0, 1, model.FailureOutputLimit, "command exceeded the stdout or stderr capture limit"), model.OutcomeError, model.FailureOutputLimit,
			`baseline test "boundary": command exceeded the stdout or stderr capture limit`, "output_limit: command exceeded the stdout or stderr capture limit"},
		{"build I/O failure", executorFailure(0, 0, model.FailureIO, "cannot read command output: input/output error"), model.OutcomeError, model.FailureIO,
			`baseline build "build-0": cannot read command output: input/output error`, "io: cannot read command output: input/output error"},
		{"running test cancelled", executorFailure(1, 1, model.FailureCancelled, "command cancelled by caller"), model.OutcomeCancelled, model.FailureCancelled,
			`candidate test "boundary": command cancelled by caller`, "cancelled: command cancelled by caller"},
		{"failure without detail", executorFailure(0, 1, model.FailureTimeout, ""), model.OutcomeError, model.FailureTimeout,
			`baseline test "boundary": timeout`, "timeout: "},
		{"unchanged build exit failure", func(r *model.Report) { *r.Variants[0].Build[0].Observation.ExitCode = 23 }, model.OutcomeError, model.FailureBuild,
			`baseline build "build-0": exit 23; expected 0`, "exit 23; expected 0"},
		{"unchanged unstarted test", func(r *model.Report) { r.Variants[1].Tests[0].Observation.Started = false }, model.OutcomeError, model.FailureLaunch,
			`candidate test "boundary": program did not start; an exit value cannot establish an expectation`, "program did not start; an exit value cannot establish an expectation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := observedReport()
			c.change(&r)
			got := evaluate.Evaluate(r)
			if got.Outcome != c.outcome || got.Failure == nil || got.Failure.Kind != c.kind || got.Failure.Message != c.message {
				t.Fatalf("aggregate %s %+v, want %s %s %q", got.Outcome, got.Failure, c.outcome, c.kind, c.message)
			}
			found := false
			for _, v := range got.Variants {
				for _, check := range append(append([]model.Check(nil), v.Build...), v.Tests...) {
					found = found || check.Status == model.CheckError && check.Reason == c.reason
				}
			}
			if !found {
				t.Fatalf("no failed check keeps reason %q: %+v", c.reason, got.Variants)
			}
		})
	}
	// Precedence is unchanged: the first failure in baseline-then-candidate
	// declaration order wins, and a report-level failure outranks both.
	r := observedReport()
	executorFailure(1, 0, model.FailureLaunch, "cannot launch command")(&r)
	executorFailure(0, 1, model.FailureTimeout, "command exceeded its configured timeout")(&r)
	if got := evaluate.Evaluate(r); got.Failure == nil || got.Failure.Message != `baseline test "boundary": command exceeded its configured timeout` {
		t.Fatalf("declaration order precedence changed: %+v", got.Failure)
	}
	r.Failure = &model.Failure{Kind: model.FailureIdentity, Message: `baseline test "boundary": artifact identity changed during testing: baseline/component`}
	if got := evaluate.Evaluate(r); got.Failure == nil || *got.Failure != *r.Failure {
		t.Fatalf("report-level failure no longer outranks check failures: %+v", got.Failure)
	}
}

func setOutput(o *model.ProcessObservation, stdout, stderr string) {
	o.Stdout = model.Capture{Data: []byte(stdout), Bytes: int64(len(stdout))}
	o.Stderr = model.Capture{Data: []byte(stderr), Bytes: int64(len(stderr))}
}

func TestNativeAndQEMUInvocationBinding(t *testing.T) {
	r := observedReport()
	r.Target.Kind = model.TargetQEMUARM
	r.Target.Executable = "/usr/bin/qemu-arm"
	r.Target.Sysroot = "/usr/arm-linux-gnueabihf"
	for i := range r.Variants {
		v := &r.Variants[i]
		v.Tests[0].Observation.Argv = append([]string{r.Target.Executable, "-L", r.Target.Sysroot}, v.Tests[0].Observation.Argv...)
		for _, list := range [][]model.ArtifactIdentity{v.ArtifactsBefore, v.ArtifactsAfter} {
			for j := range list {
				list[j].ELF = model.ELFIdentity{Class: "ELF32", Machine: "EM_ARM", Endian: "little", ABI: "arm-eabi5-hard-float"}
			}
		}
	}
	if got := evaluate.Evaluate(r); got.Outcome != model.OutcomeMatched {
		t.Fatalf("valid QEMU invocation failed: %+v", got.Failure)
	}
	for _, index := range []int{0, 1, 2, 3, 4} {
		x := observedReportWithTests(r)
		o := *x.Variants[1].Tests[0].Observation
		o.Argv = append([]string(nil), o.Argv...)
		o.Argv[index] = "wrong"
		x.Variants[1].Tests[0].Observation = &o
		if got := evaluate.Evaluate(x); got.Outcome != model.OutcomeError || evaluate.ExitCode(got) != 2 {
			t.Fatalf("QEMU prefix/artifact/args index%d accepted", index)
		}
	}
	native := observedReport()
	for i := range native.Variants {
		native.Variants[i].Tests[0].Argv[0] = "bin/component"
		native.Variants[i].Tests[0].Observation.Argv[0] = "./bin/component"
		native.Variants[i].ArtifactsBefore[0].Path = string(native.Variants[i].Name) + "/bin/component"
		native.Variants[i].ArtifactsAfter[0].Path = native.Variants[i].ArtifactsBefore[0].Path
	}
	if got := evaluate.Evaluate(native); got.Outcome != model.OutcomeMatched {
		t.Fatalf("valid nested artifact failed: %+v", got.Failure)
	}
}

func TestMissingAndErrorEvidenceNeverMatches(t *testing.T) {
	property := func(seed uint16) bool {
		r := observedReport()
		v := int(seed % 2)
		build := (seed/2)%2 == 0
		c := &r.Variants[v].Tests[0]
		if build {
			c = &r.Variants[v].Build[0]
		}
		if seed%3 == 0 {
			c.Observation = nil
			c.Status = model.CheckNotRun
		} else {
			c.Observation.Failure = &model.Failure{Kind: []model.FailureKind{model.FailureLaunch, model.FailureTimeout, model.FailureSignal, model.FailureOutputLimit}[int(seed/3)%4], Message: "injected missing or error evidence"}
			c.Status = model.CheckError
		}
		got := evaluate.Evaluate(r)
		return got.Outcome != model.OutcomeMatched && evaluate.ExitCode(got) == 2
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []model.Report{{}, {Declared: []model.VariantCoverage{{Name: model.Baseline}, {Name: model.Candidate}}}} {
		if got := evaluate.Evaluate(r); got.Outcome == model.OutcomeMatched || evaluate.ExitCode(got) != 2 {
			t.Fatal("empty evidence matched")
		}
	}
}

func TestCoverageCannotDisappearFromBothVariants(t *testing.T) {
	r := observedReport()
	for i := range r.Variants {
		r.Declared[i].TestIDs = append(r.Declared[i].TestIDs, "last")
		// The trailing declaration has no corresponding Check in either variant.
	}
	if got := evaluate.Evaluate(r); got.Outcome != model.OutcomeError || evaluate.ExitCode(got) != 2 {
		t.Fatal("symmetric omission matched")
	}
}

func TestDeclarationOrderAndRepeatEvaluation(t *testing.T) {
	r := observedReport()
	for i := range r.Variants {
		for n, id := range []string{"second", "third"} {
			r.Declared[i].TestIDs = append(r.Declared[i].TestIDs, id)
			c := r.Variants[i].Tests[0]
			c.ID = id
			c.Index = n + 1
			r.Variants[i].Tests = append(r.Variants[i].Tests, c)
		}
	}
	expected := evaluate.Evaluate(r)
	want, _ := json.Marshal(expected)
	for seed := int64(0); seed < 30; seed++ {
		shuffled := observedReportWithTests(r)
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(shuffled.Variants), func(i, j int) {
			shuffled.Variants[i], shuffled.Variants[j] = shuffled.Variants[j], shuffled.Variants[i]
		})
		for i := range shuffled.Variants {
			rng.Shuffle(len(shuffled.Variants[i].Tests), func(a, b int) {
				shuffled.Variants[i].Tests[a], shuffled.Variants[i].Tests[b] = shuffled.Variants[i].Tests[b], shuffled.Variants[i].Tests[a]
			})
		}
		got := evaluate.Evaluate(shuffled)
		data, _ := json.Marshal(got)
		if !bytes.Equal(want, data) {
			t.Fatalf("seed %d changed declared result order", seed)
		}
		again, _ := json.Marshal(evaluate.Evaluate(got))
		if !bytes.Equal(data, again) {
			t.Fatal("evaluation is not repeatable")
		}
	}
}

func observedReportWithTests(r model.Report) model.Report {
	out := r
	out.Variants = append([]model.VariantReport(nil), r.Variants...)
	for i := range out.Variants {
		out.Variants[i].Tests = append([]model.Check(nil), r.Variants[i].Tests...)
	}
	return out
}

func TestConcurrentEvaluationHasNoSharedState(t *testing.T) {
	r := observedReport()
	before := encodedInput(t, r)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := evaluate.Evaluate(r); evaluate.ExitCode(got) != 0 {
				t.Error("concurrent evaluation changed outcome")
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(before, encodedInput(t, r)) {
		t.Fatal("concurrent evaluation mutated shared input")
	}
}

func TestExitMappingFailsClosedOnContradictoryStatus(t *testing.T) {
	r := evaluate.Evaluate(observedReport())
	r.Variants[1].Tests[0].Status = model.CheckError
	if evaluate.ExitCode(r) != 2 {
		t.Fatal("aggregate matched overrode check error")
	}
	r.Variants[1].Tests[0].Status = model.CheckNotRun
	if evaluate.ExitCode(r) != 2 {
		t.Fatal("aggregate matched overrode missing check")
	}
	r.Outcome = model.OutcomeCancelled
	if evaluate.ExitCode(r) != 130 {
		t.Fatal("cancellation precedence lost")
	}
	if evaluate.ExitCode(model.Report{}) != 2 {
		t.Fatal("zero report maps to success")
	}
}

func encodedInput(t *testing.T, r model.Report) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
