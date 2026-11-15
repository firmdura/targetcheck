// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
)

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateRepresentation enforces the frozen report's field and coverage shape.
// It never compares an observed exit/marker/hash with an expected result.
func validateRepresentation(r model.Report) error {
	if r.Tool.Version == "" || r.Tool.Commit == "" || r.Tool.GoVersion == "" {
		return fmt.Errorf("report requires tool version, commit and Go version")
	}
	completed := r.Outcome == model.OutcomeMatched || r.Outcome == model.OutcomeMismatch
	if r.Target.Kind != "" && r.Target.Kind != model.TargetNative && r.Target.Kind != model.TargetQEMUARM {
		return fmt.Errorf("unsupported report target")
	}
	if completed {
		if r.Target.HostOS != "linux" || r.Target.HostArch != "amd64" && r.Target.HostArch != "arm64" || r.Target.Kind == "" {
			return fmt.Errorf("completed report requires supported Linux target metadata")
		}
		if r.Target.Kind == model.TargetQEMUARM && (r.Target.Executable == "" || r.Target.Sysroot == "") {
			return fmt.Errorf("completed ARM report requires QEMU and sysroot metadata")
		}
		if r.Manifest == nil || len(r.Inputs) == 0 {
			return fmt.Errorf("completed report requires manifest and input identities")
		}
	}
	if r.Manifest != nil && (!digest.MatchString(r.Manifest.SHA256) || r.Manifest.Encoding != "raw_json" && r.Manifest.Encoding != "canonical_json") {
		return fmt.Errorf("invalid manifest identity")
	}
	if err := validateFailure(r.Failure); err != nil {
		return err
	}
	for _, f := range r.Inputs {
		if err := validateFile(f); err != nil {
			return err
		}
	}
	for _, t := range r.Tools {
		if t.Role == "" || t.Path == "" || (t.Version == "") == (t.VersionError == "") {
			return fmt.Errorf("tool observation requires role, path and exactly one version or version_error")
		}
	}
	if len(r.Declared) > 2 || len(r.Variants) > 2 {
		return fmt.Errorf("report contains more than two variants")
	}
	seen := map[model.VariantName]bool{}
	for _, d := range r.Declared {
		if d.Name != model.Baseline && d.Name != model.Candidate || seen[d.Name] {
			return fmt.Errorf("invalid declared variant")
		}
		seen[d.Name] = true
		if len(d.BuildIDs) > model.MaxBuildSteps || len(d.TestIDs) == 0 || len(d.TestIDs) > model.MaxTests {
			return fmt.Errorf("invalid declared check count")
		}
		for _, ids := range [][]string{d.BuildIDs, d.TestIDs} {
			for _, id := range ids {
				if id == "" {
					return fmt.Errorf("empty declared check ID")
				}
			}
		}
	}
	if len(r.Declared) == 1 && r.Declared[0].Name != model.Baseline {
		return fmt.Errorf("partial declaration must begin with baseline")
	}
	if len(r.Variants) == 1 && r.Variants[0].Name != model.Baseline {
		return fmt.Errorf("partial observations must begin with baseline")
	}
	for _, v := range r.Variants {
		if !relative(v.CWD) || len(v.Tests) == 0 || len(v.Tests) > model.MaxTests || len(v.Build) > model.MaxBuildSteps {
			return fmt.Errorf("invalid variant cwd or check count")
		}
		if completed && (len(v.ArtifactsBefore) == 0 || len(v.ArtifactsAfter) == 0) {
			return fmt.Errorf("completed report requires artifact identities before and after testing")
		}
		for _, artifacts := range [][]model.ArtifactIdentity{v.ArtifactsBefore, v.ArtifactsAfter} {
			for _, a := range artifacts {
				if err := validateFile(a.FileIdentity); err != nil {
					return err
				}
				if a.ELF.Class != "ELF32" && a.ELF.Class != "ELF64" || a.ELF.Machine != "EM_ARM" && a.ELF.Machine != "EM_X86_64" && a.ELF.Machine != "EM_AARCH64" || a.ELF.Endian != "little" || a.ELF.ABI != "arm-eabi5-hard-float" && a.ELF.ABI != "linux-amd64" && a.ELF.ABI != "linux-arm64" {
					return fmt.Errorf("invalid artifact ELF metadata")
				}
			}
		}
		for _, list := range []struct {
			kind   model.CheckKind
			checks []model.Check
		}{{model.CheckBuild, v.Build}, {model.CheckTest, v.Tests}} {
			for _, c := range list.checks {
				if completed && c.Kind == model.CheckBuild && c.Status != model.CheckMatched {
					return fmt.Errorf("completed report requires matched build checks")
				}
				if c.ID == "" || c.Index < 0 || c.Kind != list.kind || c.Reason == "" || c.TimeoutSeconds < model.MinTimeoutSeconds || c.TimeoutSeconds > model.MaxTimeoutSeconds || !relative(c.CWD) || !argv(c.Argv) || !names(c.EnvNames) || c.Expectation.ExitCode < 0 || c.Expectation.ExitCode > 255 {
					return fmt.Errorf("invalid planned check fields")
				}
				if o := c.Observation; o != nil {
					if !relative(o.CWD) || !argv(o.Argv) || !names(o.EnvNames) || o.DurationMillis < 0 {
						return fmt.Errorf("invalid process observation fields")
					}
					if o.ExitCode != nil && (*o.ExitCode < 0 || *o.ExitCode > 255) {
						return fmt.Errorf("invalid observed normal exit")
					}
					if !o.Started && (o.Failure == nil || o.ExitCode != nil || o.Signal != "") {
						return fmt.Errorf("unstarted process requires failure without exit or signal")
					}
					if o.Signal != "" && (o.Failure == nil || o.ExitCode != nil) {
						return fmt.Errorf("signal observation requires failure without normal exit")
					}
					if err := validateFailure(o.Failure); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func validateFile(f model.FileIdentity) error {
	if !relative(f.Path) || !digest.MatchString(f.SHA256) || f.SizeBytes < 0 {
		return fmt.Errorf("invalid declared file identity")
	}
	return nil
}
func validateFailure(f *model.Failure) error {
	if f == nil {
		return nil
	}
	if f.Message == "" {
		return fmt.Errorf("failure requires a diagnostic")
	}
	switch f.Kind {
	case model.FailureInvalidInput, model.FailureMissingTool, model.FailureBuild, model.FailureLaunch, model.FailureSignal, model.FailureTimeout, model.FailureCancelled, model.FailureOutputLimit, model.FailureIdentity, model.FailureArtifact, model.FailureIO, model.FailureNotRun:
		return nil
	}
	return fmt.Errorf("invalid failure kind")
}
func relative(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.ContainsRune(s, 0) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func argv(values []string) bool {
	if len(values) == 0 || values[0] == "" {
		return false
	}
	for _, s := range values {
		if strings.ContainsRune(s, 0) {
			return false
		}
	}
	return true
}
func names(values []string) bool {
	for _, s := range values {
		if !envName.MatchString(s) {
			return false
		}
	}
	return true
}
