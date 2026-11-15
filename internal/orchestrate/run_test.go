//go:build linux

// SPDX-License-Identifier: Apache-2.0

package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/plan"
	"github.com/firmdura/targetcheck/internal/render"
)

type commandSpy struct{ calls int }

func (s *commandSpy) Execute(context.Context, model.CommandRequest) model.ProcessObservation {
	s.calls++
	panic("preflight launched a command")
}

func preflightWorkspace(t *testing.T) (model.Spec, model.Options) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"baseline", "candidate"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "input"), []byte("declared"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	variant := func(name string) model.Variant {
		return model.Variant{CWD: name, Build: []model.Command{{Argv: []string{"/bin/sh", "-c", "touch SHOULD_NOT_EXIST"}, TimeoutSeconds: 1}}, Inputs: []string{"input"}, Artifacts: []string{"runner"}}
	}
	return model.Spec{SchemaVersion: 1, Target: model.Target{Kind: model.TargetNative}, Variants: model.Variants{Baseline: variant("baseline"), Candidate: variant("candidate")}, TestInputs: []string{}, Tests: []model.Test{{ID: "case", Argv: []string{"runner"}, TimeoutSeconds: 1, BaselineExpect: model.Expectation{ExitCode: 0}, CandidateExpect: model.Expectation{ExitCode: 0}}}}, model.Options{Root: root}
}

func TestProductionPreflightNeverLaunches(t *testing.T) {
	tests := map[string]func(*model.Spec, *model.Options){
		"unsupported version":     func(s *model.Spec, _ *model.Options) { s.SchemaVersion = 2 },
		"duplicate tests":         func(s *model.Spec, _ *model.Options) { s.Tests = append(s.Tests, s.Tests[0]) },
		"empty argv":              func(s *model.Spec, _ *model.Options) { s.Tests[0].Argv = nil },
		"negative without marker": func(s *model.Spec, _ *model.Options) { s.Tests[0].BaselineExpect.ExitCode = 7 },
		"candidate negative":      func(s *model.Spec, _ *model.Options) { s.Tests[0].CandidateExpect.ExitCode = 7 },
		"unknown target":          func(s *model.Spec, _ *model.Options) { s.Target.Kind = "other" },
		"bad timeout":             func(s *model.Spec, _ *model.Options) { s.Tests[0].TimeoutSeconds = 0 },
		"absolute input":          func(s *model.Spec, _ *model.Options) { s.Variants.Candidate.Inputs = []string{"/etc/passwd"} },
		"traversal input":         func(s *model.Spec, _ *model.Options) { s.Variants.Candidate.Inputs = []string{"../baseline/input"} },
		"missing candidate input": func(s *model.Spec, _ *model.Options) { s.Variants.Candidate.Inputs = []string{"missing"} },
		"same cwd":                func(s *model.Spec, _ *model.Options) { s.Variants.Candidate.CWD = "baseline" },
		"root cwd":                func(s *model.Spec, _ *model.Options) { s.Variants.Baseline.CWD = "." },
		"candidate cwd inside baseline": func(s *model.Spec, o *model.Options) {
			// The nested workspace exists and is complete; only the overlap is wrong.
			if err := os.Mkdir(filepath.Join(o.Root, "baseline/candidate"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(o.Root, "baseline/candidate/input"), []byte("declared"), 0600); err != nil {
				t.Fatal(err)
			}
			s.Variants.Candidate.CWD = "baseline/candidate"
		},
		"baseline cwd inside candidate": func(s *model.Spec, _ *model.Options) { s.Variants.Baseline.CWD = "candidate/baseline" },
		"missing tool": func(s *model.Spec, _ *model.Options) {
			s.Variants.Candidate.Build[0].Argv[0] = "/missing-targetcheck-tool"
		},
		"missing qemu": func(s *model.Spec, _ *model.Options) {
			s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/missing-qemu", Sysroot: "/usr"}
		},
		"missing sysroot": func(s *model.Spec, _ *model.Options) {
			s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/sh", Sysroot: "/missing-targetcheck-sysroot"}
		},
		"relative root":         func(_ *model.Spec, o *model.Options) { o.Root = "." },
		"output inside variant": func(_ *model.Spec, o *model.Options) { o.OutputDir = filepath.Join(o.Root, "candidate/out") },
		"existing output":       func(_ *model.Spec, o *model.Options) { o.OutputDir = filepath.Join(o.Root, "baseline") },
		"symlink input": func(s *model.Spec, o *model.Options) {
			if err := os.Symlink("input", filepath.Join(o.Root, "candidate/link")); err != nil {
				t.Fatal(err)
			}
			s.Variants.Candidate.Inputs = []string{"link"}
		},
		"invalid existing ELF": func(_ *model.Spec, o *model.Options) {
			if err := os.WriteFile(filepath.Join(o.Root, "candidate/runner"), []byte("not an ELF"), 0700); err != nil {
				t.Fatal(err)
			}
		},
		"artifact symlink": func(_ *model.Spec, o *model.Options) {
			if err := os.Symlink("input", filepath.Join(o.Root, "candidate/runner")); err != nil {
				t.Fatal(err)
			}
		},
		"missing prebuilt artifact": func(s *model.Spec, _ *model.Options) { s.Variants.Candidate.Build = nil },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			spec, options := preflightWorkspace(t)
			change(&spec, &options)
			spy := &commandSpy{}
			hookCalls := 0
			r, err := (Engine{Executor: spy}).Run(context.Background(), spec, options, func() error { hookCalls++; return nil })
			var re *model.RunError
			if !errors.As(err, &re) || r.Outcome != model.OutcomeError || r.Failure == nil {
				t.Fatalf("preflight error not preserved: %v %+v", err, r)
			}
			if spy.calls != 0 || hookCalls != 0 {
				t.Fatal("preflight executed commands or reserved output")
			}
			if r.Failure.Message != re.Failure.Message || r.Failure.Kind != re.Failure.Kind {
				t.Fatal("actionable planner cause replaced")
			}
			var j, m bytes.Buffer
			if err := render.WriteJSON(&j, r); err != nil {
				t.Fatal(err)
			}
			if err := render.WriteMarkdown(&m, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMalformedLoadNeverReachesExecution(t *testing.T) {
	for _, input := range []string{`{`, `{"schema_version":1,"schema_version":1}`, strings.Repeat(" ", model.MaxManifestBytes+1)} {
		spy := &commandSpy{}
		s, err := plan.Load(strings.NewReader(input))
		if err == nil {
			_, err = (Engine{Executor: spy}).Run(context.Background(), s, model.Options{}, nil)
		}
		if err == nil || spy.calls != 0 {
			t.Fatal("malformed input reached execution")
		}
	}
}

func TestReservationFailurePreservesCompletePlanWithoutLaunch(t *testing.T) {
	spec, options := preflightWorkspace(t)
	spy := &commandSpy{}
	sentinel := errors.New("destination reservation denied")
	r, err := (Engine{Executor: spy}).Run(context.Background(), spec, options, func() error { return sentinel })
	if !errors.Is(err, sentinel) || spy.calls != 0 || len(r.Declared) != 2 || r.Variants[0].Tests[0].Status != model.CheckNotRun || r.Failure.Kind != model.FailureIO {
		t.Fatalf("reservation failure: %v %+v", err, r.Failure)
	}
	var b bytes.Buffer
	if err := render.WriteJSON(&b, r); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationAfterPrepareDoesNotReserve(t *testing.T) {
	spec, options := preflightWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &commandSpy{}
	planner := cancelPlanner{cancel: cancel}
	reserved := false
	r, err := (Engine{Planner: planner, Executor: spy}).Run(ctx, spec, options, func() error { reserved = true; return nil })
	if !errors.Is(err, context.Canceled) || r.Outcome != model.OutcomeCancelled || reserved || spy.calls != 0 {
		t.Fatalf("pre-reservation cancellation: %v %+v", err, r)
	}
}

type cancelPlanner struct {
	cancel context.CancelFunc
	plan.Service
}

func (p cancelPlanner) Prepare(s model.Spec, o model.Options) (model.Plan, error) {
	result, err := plan.Prepare(s, o)
	p.cancel()
	return result, err
}

// scriptedPlanner keeps production preflight but scripts artifact inspections.
type scriptedPlanner struct {
	plan.Service
	inspect func(call int, v model.VariantPlan) ([]model.ArtifactIdentity, error)
	calls   *int
}

func (p scriptedPlanner) InspectArtifacts(_ model.Plan, v model.VariantPlan) ([]model.ArtifactIdentity, error) {
	*p.calls++
	return p.inspect(*p.calls, v)
}

// scriptedExecutor returns normal exits, or the scripted failure for one ID.
type scriptedExecutor struct {
	failID  string
	failure model.Failure
}

func (e scriptedExecutor) Execute(_ context.Context, request model.CommandRequest) model.ProcessObservation {
	names := []string{}
	for name := range request.Env {
		names = append(names, name)
	}
	o := model.ProcessObservation{Started: true, Argv: append([]string{}, request.Argv...), CWD: request.RelativeCWD, EnvNames: names}
	if request.ID == e.failID {
		f := e.failure
		o.Failure = &f
		return o
	}
	exit := 0
	o.ExitCode = &exit
	return o
}

// A failure of the after-testing artifact inspection voids every test that ran
// in that variant, so the aggregate failure names them, while precedence and
// the checks' own reasons stay unchanged.
func TestAfterTestingArtifactFailuresNameTheTestedChecks(t *testing.T) {
	identity := func(v model.VariantPlan, paths []string, digit string) []model.ArtifactIdentity {
		var ids []model.ArtifactIdentity
		for _, p := range paths {
			ids = append(ids, model.ArtifactIdentity{FileIdentity: model.FileIdentity{Path: v.CWD + "/" + p, SHA256: strings.Repeat(digit, 64), SizeBytes: 1}, ELF: model.ELFIdentity{Class: "ELF64", Machine: "EM_X86_64", Endian: "little", ABI: "linux-amd64"}})
		}
		return ids
	}
	changed := func(v model.VariantPlan, paths []string) ([]model.ArtifactIdentity, error) {
		return identity(v, paths, "2"), nil
	}
	missing := func(model.VariantPlan, []string) ([]model.ArtifactIdentity, error) {
		return nil, &model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureArtifact, Message: "artifact baseline/runner: no such file or directory"}}
	}
	timeout := scriptedExecutor{failID: "t1", failure: model.Failure{Kind: model.FailureTimeout, Message: "command exceeded its configured timeout"}}
	cases := []struct {
		name      string
		tests     int
		artifacts []string
		after     func(model.VariantPlan, []string) ([]model.ArtifactIdentity, error)
		executor  scriptedExecutor
		kind      model.FailureKind
		message   string
	}{
		{"changed after one test", 1, []string{"runner"}, changed, scriptedExecutor{}, model.FailureIdentity,
			`baseline test "t0": artifact identity changed during testing: baseline/runner`},
		{"changed after three tests", 3, []string{"runner"}, changed, scriptedExecutor{}, model.FailureIdentity,
			`baseline tests "t0" to "t2": artifact identity changed during testing: baseline/runner`},
		{"several changed artifacts", 2, []string{"runner", "zz-helper", "aa-helper"}, changed, scriptedExecutor{}, model.FailureIdentity,
			`baseline tests "t0" to "t1": artifact identity changed during testing: baseline/aa-helper and 2 more`},
		{"missing after testing", 2, []string{"runner"}, missing, scriptedExecutor{}, model.FailureArtifact,
			`baseline tests "t0" to "t1": artifact baseline/runner: no such file or directory`},
		// The test timeout stops testing; the report-level identity failure still
		// outranks it, as before, and names the tests that ran.
		{"changed after a test timeout", 3, []string{"runner"}, changed, timeout, model.FailureIdentity,
			`baseline tests "t0" to "t1": artifact identity changed during testing: baseline/runner`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, options := preflightWorkspace(t)
			spec.Tests = nil
			for i := 0; i < c.tests; i++ {
				spec.Tests = append(spec.Tests, model.Test{ID: fmt.Sprintf("t%d", i), Argv: []string{"runner"}, TimeoutSeconds: 1, BaselineExpect: model.Expectation{ExitCode: 0}, CandidateExpect: model.Expectation{ExitCode: 0}})
			}
			for _, v := range []*model.Variant{&spec.Variants.Baseline, &spec.Variants.Candidate} {
				v.Artifacts = c.artifacts
			}
			calls := 0
			planner := scriptedPlanner{calls: &calls, inspect: func(call int, v model.VariantPlan) ([]model.ArtifactIdentity, error) {
				if call == 1 {
					return identity(v, c.artifacts, "1"), nil
				}
				return c.after(v, c.artifacts)
			}}
			r, err := (Engine{Planner: planner, Executor: c.executor}).Run(context.Background(), spec, options, nil)
			var re *model.RunError
			if !errors.As(err, &re) || r.Outcome != model.OutcomeError || r.Failure == nil || r.Failure.Kind != c.kind || r.Failure.Message != c.message || re.Failure != *r.Failure {
				t.Fatalf("got %v %+v, want %s %q", err, r.Failure, c.kind, c.message)
			}
			if calls != 2 || r.Variants[1].Build[0].Observation != nil {
				t.Fatalf("candidate continued after the baseline failure (%d inspections)", calls)
			}
			for _, check := range r.Variants[0].Tests {
				if check.Observation != nil && check.Status != model.CheckError {
					t.Fatalf("tested check %s kept status %s", check.ID, check.Status)
				}
				if check.ID == c.executor.failID && check.Reason != "timeout: command exceeded its configured timeout" {
					t.Fatalf("per-check reason changed: %q", check.Reason)
				}
			}
		})
	}
}
