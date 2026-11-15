// SPDX-License-Identifier: Apache-2.0

// Package orchestrate joins the validated plan, executor and pure evaluator.
// It holds all run state per invocation. Only the CLI supplies a reservation hook.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/buildinfo"
	"github.com/firmdura/targetcheck/internal/evaluate"
	"github.com/firmdura/targetcheck/internal/execute"
	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/plan"
)

const (
	// qemuVersionTimeoutSeconds bounds the single QEMU version query.
	qemuVersionTimeoutSeconds = 10
	// maxVersionBytes bounds the recorded version line.
	maxVersionBytes = 256
	// qemuToolRole is the planner's role name for the configured emulator.
	qemuToolRole   = "qemu"
	qemuNotQueried = "not queried: the run stopped before the --version query"
)

// Engine permits command spies without replacing production preflight.
// Zero-valued dependencies select the real components.
type Engine struct {
	Planner  model.Planner
	Executor model.Executor
}

// FailureReport preserves an early actionable error without inventing coverage.
func FailureReport(err error) model.Report {
	r := emptyReport()
	var runErr *model.RunError
	if !errors.As(err, &runErr) {
		runErr = &model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureInvalidInput, Message: err.Error()}, Cause: err}
	}
	r.Failure = &runErr.Failure
	if runErr.Kind == model.ErrorCancelled {
		r.Outcome = model.OutcomeCancelled
	}
	return r
}

func emptyReport() model.Report {
	return model.Report{SchemaVersion: model.SchemaVersion, Tool: buildinfo.Info(), Target: model.TargetInfo{HostOS: runtime.GOOS, HostArch: runtime.GOARCH}, Inputs: []model.FileIdentity{}, Tools: []model.ToolIdentity{}, Declared: []model.VariantCoverage{}, Variants: []model.VariantReport{}, Outcome: model.OutcomeError}
}

// Run invokes beforeExecute exactly once after successful preflight and before
// any command. The CLI reserves its destination here; public Run supplies nil.
func (e Engine) Run(ctx context.Context, spec model.Spec, options model.Options, beforeExecute func() error) (model.Report, error) {
	if ctx == nil {
		err := &model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureInvalidInput, Message: "context is required"}}
		return FailureReport(err), err
	}
	if ctx.Err() != nil {
		err := cancelled(ctx)
		return FailureReport(err), err
	}
	planner := e.Planner
	if planner == nil {
		planner = plan.Service{}
	}
	executor := e.Executor
	if executor == nil {
		executor = execute.Executor{}
	}
	p, err := planner.Prepare(spec, options)
	if err != nil {
		if ctx.Err() != nil {
			err = cancelled(ctx)
		}
		return FailureReport(err), err
	}
	r := plannedReport(p)
	if ctx.Err() != nil {
		err = cancelled(ctx)
		r.Failure = &err.(*model.RunError).Failure
		return finish(ctx, r, err)
	}
	stop := func(err error) {
		var re *model.RunError
		if errors.As(err, &re) {
			f := re.Failure
			r.Failure = &f
		} else {
			r.Failure = &model.Failure{Kind: model.FailureIO, Message: err.Error()}
		}
	}
	if beforeExecute != nil {
		if err = beforeExecute(); err != nil {
			stop(err)
			return finish(ctx, r, err)
		}
	}
	if ctx.Err() != nil {
		err = cancelled(ctx)
		stop(err)
		return finish(ctx, r, err)
	}
	if p.Target.Kind == model.TargetQEMUARM {
		// Besides the declared commands, this is the only process a run starts.
		version, versionError := queryQEMUVersion(ctx, executor, p, qemuVersionTimeoutSeconds)
		recordQEMUVersion(&r, version, versionError)
		if ctx.Err() != nil {
			err = cancelled(ctx)
			stop(err)
			return finish(ctx, r, err)
		}
	}
	halted := false
	for i, vp := range p.Variants {
		v := &r.Variants[i]
		for j, request := range vp.Build {
			if ctx.Err() != nil {
				err = cancelled(ctx)
				stop(err)
				halted = true
				break
			}
			o := executor.Execute(ctx, request)
			v.Build[j].Observation = &o
			if o.Failure != nil || !o.Started || o.Signal != "" || o.ExitCode == nil || *o.ExitCode != 0 {
				halted = true
				break
			}
		}
		if halted {
			break
		}
		v.ArtifactsBefore, err = planner.InspectArtifacts(p, vp)
		if err != nil {
			stop(err)
			halted = true
			break
		}
		for j, request := range vp.Tests {
			if ctx.Err() != nil {
				err = cancelled(ctx)
				stop(err)
				halted = true
				break
			}
			o := executor.Execute(ctx, request)
			v.Tests[j].Observation = &o
			if o.Failure != nil || !o.Started || o.Signal != "" || o.ExitCode == nil {
				halted = true
				break
			}
		}
		// Retain the final identity observation even after a command error/cancel.
		// Its failures void the verdicts of every test that ran, so they name those.
		v.ArtifactsAfter, err = planner.InspectArtifacts(p, vp)
		if err != nil {
			stop(err)
			r.Failure.Message = testedChecks(*v) + ": " + r.Failure.Message
			halted = true
		}
		if !slices.Equal(v.ArtifactsBefore, v.ArtifactsAfter) && err == nil {
			err = &model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureIdentity, Message: testedChecks(*v) + ": artifact identity changed during testing: " + changedArtifacts(v.ArtifactsBefore, v.ArtifactsAfter)}}
			stop(err)
			halted = true
		}
		if halted {
			break
		}
	}
	return finish(ctx, r, err)
}

func finish(ctx context.Context, r model.Report, cause error) (model.Report, error) {
	if ctx.Err() != nil {
		cause = cancelled(ctx)
		r.Failure = &cause.(*model.RunError).Failure
	}
	r = evaluate.Evaluate(r)
	if r.Outcome == model.OutcomeMatched || r.Outcome == model.OutcomeMismatch {
		return r, nil
	}
	kind := model.ErrorInfrastructure
	if r.Outcome == model.OutcomeCancelled {
		kind = model.ErrorCancelled
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
	}
	return r, &model.RunError{Kind: kind, Failure: *r.Failure, Cause: cause}
}

func cancelled(ctx context.Context) error {
	return &model.RunError{Kind: model.ErrorCancelled, Failure: model.Failure{Kind: model.FailureCancelled, Message: "run cancelled by caller"}, Cause: ctx.Err()}
}

func plannedReport(p model.Plan) model.Report {
	r := emptyReport()
	r.Target = p.Target
	r.Manifest = &p.Manifest
	r.Inputs = p.Inputs
	r.Tools = slices.Clone(p.Tools)
	// Until the post-preflight query records an observation, none was made.
	recordQEMUVersion(&r, "", qemuNotQueried)
	// Declaration comes from the validated Spec, independently of the plan checks.
	for i, declaration := range []model.Variant{p.Spec.Variants.Baseline, p.Spec.Variants.Candidate} {
		name := model.Baseline
		if i == 1 {
			name = model.Candidate
		}
		coverage := model.VariantCoverage{Name: name, BuildIDs: []string{}, TestIDs: []string{}}
		for j := range declaration.Build {
			coverage.BuildIDs = append(coverage.BuildIDs, fmt.Sprintf("build-%d", j))
		}
		for _, t := range p.Spec.Tests {
			coverage.TestIDs = append(coverage.TestIDs, t.ID)
		}
		r.Declared = append(r.Declared, coverage)
	}
	for _, vp := range p.Variants {
		v := model.VariantReport{Name: vp.Name, CWD: vp.CWD, Build: []model.Check{}, Tests: []model.Check{}, ArtifactsBefore: []model.ArtifactIdentity{}, ArtifactsAfter: []model.ArtifactIdentity{}}
		for i, request := range vp.Build {
			v.Build = append(v.Build, check(request, i, model.Expectation{ExitCode: 0}))
		}
		for i, request := range vp.Tests {
			expect := p.Spec.Tests[i].BaselineExpect
			if vp.Name == model.Candidate {
				expect = p.Spec.Tests[i].CandidateExpect
			}
			v.Tests = append(v.Tests, check(request, i, expect))
		}
		r.Variants = append(r.Variants, v)
	}
	return r
}
func check(request model.CommandRequest, index int, expect model.Expectation) model.Check {
	names := make([]string, 0, len(request.Env))
	for name := range request.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	return model.Check{ID: request.ID, Index: index, Kind: request.Kind, Argv: slices.Clone(request.Argv), CWD: request.RelativeCWD, EnvNames: names, TimeoutSeconds: request.TimeoutSeconds, Expectation: expect, Status: model.CheckNotRun, Reason: "no process observation; expectation not established"}
}

// testedChecks names the tests whose verdicts a failure of the after-testing
// artifact inspection voids: the variant's tests run in declaration order, from
// the first to the last one observed before the artifacts were inspected again.
func testedChecks(v model.VariantReport) string {
	last := -1
	for i, c := range v.Tests {
		if c.Observation != nil {
			last = i
		}
	}
	switch last {
	case -1:
		return string(v.Name)
	case 0:
		return fmt.Sprintf("%s test %q", v.Name, v.Tests[0].ID)
	default:
		return fmt.Sprintf("%s tests %q to %q", v.Name, v.Tests[0].ID, v.Tests[last].ID)
	}
}

// changedArtifacts names, in path order, the artifacts whose identity differs
// between the inspections before and after testing.
func changedArtifacts(before, after []model.ArtifactIdentity) string {
	previous := make(map[string]model.ArtifactIdentity, len(before))
	for _, a := range before {
		previous[a.Path] = a
	}
	var changed []string
	for _, a := range after {
		if old, ok := previous[a.Path]; !ok || old != a {
			changed = append(changed, a.Path)
		}
		delete(previous, a.Path)
	}
	for path := range previous {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	switch len(changed) {
	case 0:
		return "declared artifacts"
	case 1:
		return changed[0]
	default:
		return fmt.Sprintf("%s and %d more", changed[0], len(changed)-1)
	}
}

// queryQEMUVersion runs the configured emulator once, as QEMU --version,
// through the normal executor: in its own process group, with the inherited
// environment and no overrides, in Root, with the standard capture limits and
// the given timeout. Like a build command, the query runs directly on the host.
// It returns the version line or why none was recorded; version evidence never
// fails a run.
func queryQEMUVersion(ctx context.Context, executor model.Executor, p model.Plan, timeoutSeconds int) (version, versionError string) {
	request := model.CommandRequest{Kind: model.CheckBuild, ID: "qemu-version", Target: p.Target, Argv: []string{p.Target.Executable, "--version"}, ExecutablePath: p.Target.Executable, AbsoluteCWD: p.Root, RelativeCWD: ".", TimeoutSeconds: timeoutSeconds}
	return versionLine(executor.Execute(ctx, request), timeoutSeconds)
}

// versionLine accepts only the first non-empty stdout line of a normal zero
// exit, trimmed, valid UTF-8 and at most maxVersionBytes long.
func versionLine(o model.ProcessObservation, timeoutSeconds int) (version, versionError string) {
	const query = "--version query"
	switch {
	case o.Failure != nil:
		detail := string(o.Failure.Kind) + ": " + o.Failure.Message
		if o.Failure.Kind == model.FailureTimeout {
			detail += fmt.Sprintf(" (%d s)", timeoutSeconds)
		}
		return "", query + " failed: " + detail
	case !o.Started || o.Signal != "" || o.ExitCode == nil:
		return "", query + " failed: no normal exit was observed"
	case *o.ExitCode != 0:
		return "", fmt.Sprintf("%s failed: exit %d; expected 0", query, *o.ExitCode)
	}
	for _, line := range strings.Split(string(o.Stdout.Data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case len(line) > maxVersionBytes:
			return "", fmt.Sprintf("%s printed a first non-empty stdout line longer than %d bytes", query, maxVersionBytes)
		case !utf8.ValidString(line):
			return "", query + " printed a first non-empty stdout line that is not valid UTF-8"
		}
		return line, ""
	}
	return "", query + " printed no non-empty stdout line"
}

// recordQEMUVersion sets exactly one of version and version_error on the qemu
// tool observation.
func recordQEMUVersion(r *model.Report, version, versionError string) {
	for i := range r.Tools {
		if r.Tools[i].Role == qemuToolRole {
			r.Tools[i].Version, r.Tools[i].VersionError = version, versionError
		}
	}
}
