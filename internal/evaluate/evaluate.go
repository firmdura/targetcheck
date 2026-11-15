// SPDX-License-Identifier: Apache-2.0

// Package evaluate derives behavioral outcomes from recorded observations. It
// does not execute commands, inspect files, or render reports.
package evaluate

import (
	"bytes"
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
)

// Evaluator implements the frozen component interface. Its zero value is ready
// to use and holds no mutable state.
type Evaluator struct{}

var _ model.Evaluator = Evaluator{}

func (Evaluator) Evaluate(r model.Report) model.Report { return Evaluate(r) }
func (Evaluator) ExitCode(r model.Report) int          { return ExitCode(r) }

// Evaluate returns an evaluated copy, preserving every received observation.
// Incoming outcome/status labels are derived data and are recomputed. Explicit
// report failures remain evidence, including failures outside a child process.
// Raw capture byte slices are read only and are shared with the input.
func Evaluate(r model.Report) model.Report {
	out := copyReport(r)
	state := assessment{}
	state.record(out.Failure)
	if err := model.ValidateCoverage(out); err != nil {
		state.record(failure(model.FailureInvalidInput, "invalid declared coverage: "+err.Error()))
	}
	if err := validateEvidence(out); err != nil {
		state.record(failure(model.FailureInvalidInput, "invalid report evidence: "+err.Error()))
	}
	for i := range out.Variants {
		v := &out.Variants[i]
		for j := range v.Build {
			f := evaluateCheck(&v.Build[j], v.Name, v.CWD, out.Target)
			state.record(f)
		}
		for j := range v.Tests {
			f := evaluateCheck(&v.Tests[j], v.Name, v.CWD, out.Target)
			state.record(f)
			state.mismatch = state.mismatch || v.Tests[j].Status == model.CheckMismatch
		}
		if f := inspectIdentity(*v, out.Target); f != nil {
			state.record(f)
			for j := range v.Tests {
				if v.Tests[j].Observation != nil && v.Tests[j].Status != model.CheckError {
					v.Tests[j].Status = model.CheckError
					v.Tests[j].Reason = f.Message
				}
			}
		}
	}
	switch {
	case state.cancelled != nil:
		out.Outcome, out.Failure = model.OutcomeCancelled, state.cancelled
	case state.infrastructure != nil:
		out.Outcome, out.Failure = model.OutcomeError, state.infrastructure
	case state.mismatch:
		out.Outcome, out.Failure = model.OutcomeMismatch, nil
	default:
		out.Outcome, out.Failure = model.OutcomeMatched, nil
	}
	return out
}

// ExitCode maps an evaluated report to CLI policy. Contradictory or incomplete
// aggregate/check states fail closed, without re-reading captures or evaluating
// already serialized output. Call Evaluate before using this function.
func ExitCode(r model.Report) int {
	if r.Outcome == model.OutcomeCancelled || (r.Failure != nil && r.Failure.Kind == model.FailureCancelled) {
		return 130
	}
	if r.Failure != nil || model.ValidateCoverage(r) != nil {
		return 2
	}
	mismatch := false
	for _, v := range r.Variants {
		for _, group := range [][]model.Check{v.Build, v.Tests} {
			for _, c := range group {
				switch c.Status {
				case model.CheckError, model.CheckNotRun:
					return 2
				case model.CheckMismatch:
					if c.Kind == model.CheckBuild {
						return 2
					}
					mismatch = true
				case model.CheckMatched:
				default:
					return 2
				}
			}
		}
	}
	if r.Outcome == model.OutcomeError {
		return 2
	}
	if r.Outcome == model.OutcomeMismatch && mismatch {
		return 1
	}
	if r.Outcome == model.OutcomeMatched && !mismatch {
		return 0
	}
	return 2
}

type assessment struct {
	cancelled      *model.Failure
	infrastructure *model.Failure
	mismatch       bool
}

func (s *assessment) record(f *model.Failure) {
	if f == nil {
		return
	}
	copy := *f
	if copy.Kind == "" {
		copy.Kind = model.FailureInvalidInput
	}
	if copy.Message == "" {
		copy.Message = string(copy.Kind)
	}
	if copy.Kind == model.FailureCancelled {
		if s.cancelled == nil {
			s.cancelled = &copy
		}
	} else if s.infrastructure == nil {
		s.infrastructure = &copy
	}
}

func evaluateCheck(c *model.Check, variant model.VariantName, cwd string, target model.TargetInfo) *model.Failure {
	if c.Observation == nil {
		c.Status, c.Reason = model.CheckNotRun, "no process observation; expectation not established"
		return failure(model.FailureNotRun, checkName(*c, variant)+": "+c.Reason)
	}
	o := c.Observation
	if o.Failure != nil {
		c.Status = model.CheckError
		c.Reason = string(o.Failure.Kind) + ": " + o.Failure.Message
		// Like every other check failure, the aggregate failure names the check.
		message := cmp.Or(o.Failure.Message, string(o.Failure.Kind), "unclassified failure")
		return failure(o.Failure.Kind, checkName(*c, variant)+": "+message)
	}
	var f *model.Failure
	switch {
	case !o.Started:
		f = failure(model.FailureLaunch, "program did not start; an exit value cannot establish an expectation")
	case o.Signal != "":
		f = failure(model.FailureSignal, "program terminated by signal "+o.Signal)
	case o.ExitCode == nil || *o.ExitCode < 0 || *o.ExitCode > 255:
		f = failure(model.FailureInvalidInput, "started command has no valid normal exit")
	case o.DurationMillis < 0:
		f = failure(model.FailureInvalidInput, "negative command duration")
	}
	if f == nil {
		f = validateInvocation(*c, cwd, target)
	}
	if f == nil {
		for _, stream := range []struct {
			name string
			cap  model.Capture
		}{{"stdout", o.Stdout}, {"stderr", o.Stderr}} {
			if stream.cap.Truncated || stream.cap.Bytes > model.MaxCaptureBytes || len(stream.cap.Data) > model.MaxCaptureBytes {
				f = failure(model.FailureOutputLimit, stream.name+" capture exceeded its limit")
				break
			}
			if stream.cap.Bytes != int64(len(stream.cap.Data)) {
				f = failure(model.FailureIO, stream.name+" capture bytes are incomplete or inconsistent")
				break
			}
		}
	}
	if f == nil {
		f = validateExpectation(*c, variant)
	}
	if f != nil {
		c.Status, c.Reason = model.CheckError, f.Message
		return failure(f.Kind, checkName(*c, variant)+": "+f.Message)
	}
	var reasons []string
	if *o.ExitCode != c.Expectation.ExitCode {
		reasons = append(reasons, fmt.Sprintf("exit %d; expected %d", *o.ExitCode, c.Expectation.ExitCode))
	}
	if marker := c.Expectation.StdoutContains; marker != "" && !bytes.Contains(o.Stdout.Data, []byte(marker)) {
		reasons = append(reasons, fmt.Sprintf("stdout missing literal marker %q", marker))
	}
	if marker := c.Expectation.StderrContains; marker != "" && !bytes.Contains(o.Stderr.Data, []byte(marker)) {
		reasons = append(reasons, fmt.Sprintf("stderr missing literal marker %q", marker))
	}
	if len(reasons) == 0 {
		c.Status, c.Reason = model.CheckMatched, "normal exit and all required literal markers matched"
		return nil
	}
	c.Reason = strings.Join(reasons, "; ")
	if c.Kind == model.CheckBuild {
		c.Status = model.CheckError
		return failure(model.FailureBuild, checkName(*c, variant)+": "+c.Reason)
	}
	c.Status = model.CheckMismatch
	return nil
}

func validateInvocation(c model.Check, cwd string, target model.TargetInfo) *model.Failure {
	o := c.Observation
	if !relativePath(c.CWD) || c.CWD != cwd || o.CWD != c.CWD {
		return failure(model.FailureInvalidInput, "observed cwd differs from planned variant cwd")
	}
	names, observedNames := slices.Clone(c.EnvNames), slices.Clone(o.EnvNames)
	slices.Sort(names)
	slices.Sort(observedNames)
	if !slices.Equal(names, observedNames) {
		return failure(model.FailureInvalidInput, "observed environment override names differ from the plan")
	}
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return failure(model.FailureInvalidInput, "planned argv has no executable")
	}
	if c.Kind == model.CheckBuild {
		if !slices.Equal(c.Argv, o.Argv) {
			return failure(model.FailureInvalidInput, "observed build argv differs from the plan")
		}
		return nil
	}
	actual := o.Argv
	if target.Kind == model.TargetQEMUARM {
		prefix := []string{target.Executable, "-L", target.Sysroot}
		if len(actual) < len(prefix) || !slices.Equal(actual[:len(prefix)], prefix) {
			return failure(model.FailureInvalidInput, "observed QEMU executable or sysroot prefix differs from the plan")
		}
		actual = actual[len(prefix):]
	}
	if len(actual) != len(c.Argv) || len(actual) == 0 || !relativePath(c.Argv[0]) || !relativePath(actual[0]) ||
		path.Clean(actual[0]) != path.Clean(c.Argv[0]) || !strings.Contains(actual[0], "/") || !slices.Equal(actual[1:], c.Argv[1:]) {
		return failure(model.FailureInvalidInput, "observed test artifact or arguments differ from the plan")
	}
	return nil
}

func validateExpectation(c model.Check, variant model.VariantName) *model.Failure {
	e := c.Expectation
	switch {
	case e.ExitCode < 0 || e.ExitCode > 255:
		return failure(model.FailureInvalidInput, "expectation exit must be in 0..255")
	case c.Kind == model.CheckBuild && (e.ExitCode != 0 || e.StdoutContains != "" || e.StderrContains != ""):
		return failure(model.FailureInvalidInput, "build expectation must require only exit 0")
	case c.Kind == model.CheckTest && variant == model.Candidate && e.ExitCode != 0:
		return failure(model.FailureInvalidInput, "candidate expectation must require exit 0")
	case c.Kind == model.CheckTest && variant == model.Baseline && e.ExitCode != 0 && e.StdoutContains == "" && e.StderrContains == "":
		return failure(model.FailureInvalidInput, "nonzero baseline expectation requires a nonempty literal marker")
	}
	return nil
}

func validateEvidence(r model.Report) error {
	if r.SchemaVersion != model.SchemaVersion || r.Manifest == nil || !validDigest(r.Manifest.SHA256) {
		return fmt.Errorf("schema version and manifest identity are required")
	}
	if r.Manifest.Encoding != "raw_json" && r.Manifest.Encoding != "canonical_json" {
		return fmt.Errorf("unsupported manifest identity encoding")
	}
	if r.Target.HostOS != "linux" || (r.Target.HostArch != "amd64" && r.Target.HostArch != "arm64") {
		return fmt.Errorf("supported Linux host identity is required")
	}
	if r.Target.Kind != model.TargetNative && r.Target.Kind != model.TargetQEMUARM {
		return fmt.Errorf("supported target identity is required")
	}
	if r.Target.Kind == model.TargetQEMUARM && (r.Target.Executable == "" || r.Target.Sysroot == "") {
		return fmt.Errorf("QEMU executable and sysroot identity are required")
	}
	if r.Tool.Version == "" || r.Tool.Commit == "" || r.Tool.GoVersion == "" {
		return fmt.Errorf("tool version identity is required")
	}
	if len(r.Inputs) == 0 {
		return fmt.Errorf("declared input identities are required")
	}
	seen := make(map[string]bool, len(r.Inputs))
	for _, input := range r.Inputs {
		if !validFile(input) || seen[input.Path] {
			return fmt.Errorf("invalid or duplicate input identity %q", input.Path)
		}
		seen[input.Path] = true
	}
	return nil
}

func inspectIdentity(v model.VariantReport, target model.TargetInfo) *model.Failure {
	tested := false
	for _, c := range v.Tests {
		tested = tested || c.Observation != nil
	}
	if !tested && len(v.ArtifactsBefore) == 0 && len(v.ArtifactsAfter) == 0 {
		return nil // The missing observations already prevent a matched result.
	}
	if len(v.ArtifactsBefore) == 0 || len(v.ArtifactsAfter) == 0 {
		return failure(model.FailureArtifact, string(v.Name)+": artifact identities before and after testing are required")
	}
	before := make(map[string]model.ArtifactIdentity, len(v.ArtifactsBefore))
	for _, a := range v.ArtifactsBefore {
		if !validFile(a.FileIdentity) || !validELF(a.ELF, target) {
			return failure(model.FailureArtifact, string(v.Name)+": invalid artifact identity "+a.Path)
		}
		key := path.Clean(a.Path)
		if _, exists := before[key]; exists {
			return failure(model.FailureArtifact, string(v.Name)+": duplicate artifact identity "+a.Path)
		}
		before[key] = a
	}
	seen := make(map[string]bool, len(v.ArtifactsAfter))
	for _, a := range v.ArtifactsAfter {
		key := path.Clean(a.Path)
		if !validFile(a.FileIdentity) || !validELF(a.ELF, target) || seen[key] {
			return failure(model.FailureArtifact, string(v.Name)+": invalid or duplicate final artifact identity "+a.Path)
		}
		seen[key] = true
		old, exists := before[key]
		if !exists || old != a {
			return failure(model.FailureIdentity, string(v.Name)+": artifact identity changed: "+a.Path)
		}
	}
	if len(before) != len(seen) {
		return failure(model.FailureIdentity, string(v.Name)+": artifact identity coverage changed after testing")
	}
	for _, c := range v.Tests {
		if c.Observation == nil || !c.Observation.Started {
			continue
		}
		if len(c.Argv) == 0 || !relativePath(c.Argv[0]) || !relativePath(c.CWD) {
			return failure(model.FailureArtifact, string(v.Name)+": tested artifact path is invalid")
		}
		if _, exists := before[path.Join(c.CWD, c.Argv[0])]; !exists {
			return failure(model.FailureArtifact, string(v.Name)+": identities do not cover tested artifact "+path.Join(c.CWD, c.Argv[0]))
		}
	}
	return nil
}

func validELF(elf model.ELFIdentity, target model.TargetInfo) bool {
	if elf.Endian != "little" {
		return false
	}
	if target.Kind == model.TargetQEMUARM {
		return elf.Class == "ELF32" && elf.Machine == "EM_ARM" && elf.ABI == "arm-eabi5-hard-float"
	}
	switch target.HostArch {
	case "amd64":
		return elf.Class == "ELF64" && elf.Machine == "EM_X86_64" && elf.ABI == "linux-amd64"
	case "arm64":
		return elf.Class == "ELF64" && elf.Machine == "EM_AARCH64" && elf.ABI == "linux-arm64"
	default:
		return false
	}
}

func validFile(f model.FileIdentity) bool {
	return relativePath(f.Path) && f.SizeBytes >= 0 && validDigest(f.SHA256)
}

func relativePath(p string) bool {
	if p == "" || path.Clean(p) == "." || path.IsAbs(p) || strings.ContainsRune(p, 0) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func failure(kind model.FailureKind, message string) *model.Failure {
	return &model.Failure{Kind: kind, Message: message}
}

func checkName(c model.Check, variant model.VariantName) string {
	return fmt.Sprintf("%s %s %q", variant, c.Kind, c.ID)
}

func copyReport(r model.Report) model.Report {
	if r.Manifest != nil {
		manifest := *r.Manifest
		r.Manifest = &manifest
	}
	r.Inputs = slices.Clone(r.Inputs)
	r.Tools = slices.Clone(r.Tools)
	r.Declared = slices.Clone(r.Declared)
	for i := range r.Declared {
		r.Declared[i].BuildIDs = slices.Clone(r.Declared[i].BuildIDs)
		r.Declared[i].TestIDs = slices.Clone(r.Declared[i].TestIDs)
	}
	r.Variants = slices.Clone(r.Variants)
	for i := range r.Variants {
		v := &r.Variants[i]
		v.Build, v.Tests = copyChecks(v.Build), copyChecks(v.Tests)
		v.ArtifactsBefore, v.ArtifactsAfter = slices.Clone(v.ArtifactsBefore), slices.Clone(v.ArtifactsAfter)
		slices.SortStableFunc(v.Build, func(a, b model.Check) int { return cmp.Compare(a.Index, b.Index) })
		slices.SortStableFunc(v.Tests, func(a, b model.Check) int { return cmp.Compare(a.Index, b.Index) })
		slices.SortStableFunc(v.ArtifactsBefore, func(a, b model.ArtifactIdentity) int { return strings.Compare(a.Path, b.Path) })
		slices.SortStableFunc(v.ArtifactsAfter, func(a, b model.ArtifactIdentity) int { return strings.Compare(a.Path, b.Path) })
	}
	slices.SortStableFunc(r.Inputs, func(a, b model.FileIdentity) int { return strings.Compare(a.Path, b.Path) })
	slices.SortStableFunc(r.Declared, func(a, b model.VariantCoverage) int { return compareVariants(a.Name, b.Name) })
	slices.SortStableFunc(r.Variants, func(a, b model.VariantReport) int { return compareVariants(a.Name, b.Name) })
	return r
}

func copyChecks(checks []model.Check) []model.Check {
	out := slices.Clone(checks)
	for i := range out {
		out[i].Argv, out[i].EnvNames = slices.Clone(out[i].Argv), slices.Clone(out[i].EnvNames)
		if out[i].Observation != nil {
			o := *out[i].Observation
			o.Argv, o.EnvNames = slices.Clone(o.Argv), slices.Clone(o.EnvNames)
			if o.ExitCode != nil {
				exit := *o.ExitCode
				o.ExitCode = &exit
			}
			if o.Failure != nil {
				f := *o.Failure
				o.Failure = &f
			}
			out[i].Observation = &o
		}
	}
	return out
}

func compareVariants(a, b model.VariantName) int {
	order := func(v model.VariantName) int {
		switch v {
		case model.Baseline:
			return 0
		case model.Candidate:
			return 1
		default:
			return 2
		}
	}
	if n := cmp.Compare(order(a), order(b)); n != 0 {
		return n
	}
	return strings.Compare(string(a), string(b))
}
