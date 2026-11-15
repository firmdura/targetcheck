// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/model"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateSpec(s model.Spec) error {
	if s.SchemaVersion != model.SchemaVersion {
		return invalid("unsupported schema_version", nil)
	}
	switch s.Target.Kind {
	case model.TargetNative:
		if s.Target.Executable != "" || s.Target.Sysroot != "" {
			return invalid("native target has QEMU-only fields", nil)
		}
	case model.TargetQEMUARM:
		if !validText(s.Target.Executable) || !validText(s.Target.Sysroot) {
			return invalid("qemu-arm requires executable and sysroot", nil)
		}
	default:
		return invalid("unsupported target kind", nil)
	}
	if len(s.Tests) == 0 || len(s.Tests) > model.MaxTests {
		return invalid("tests must contain 1..256 cases", nil)
	}
	names := map[string]bool{}
	for _, test := range s.Tests {
		if test.ID == "" || !utf8.ValidString(test.ID) || names[test.ID] {
			return invalid("empty, invalid or duplicate test ID", nil)
		}
		names[test.ID] = true
		if err := argv(test.Argv); err != nil {
			return err
		}
		if _, err := relativePath(test.Argv[0]); err != nil {
			return invalid("test executable must be variant-relative", err)
		}
		if !timeout(test.TimeoutSeconds) {
			return invalid("test timeout must be 1..3600 seconds", nil)
		}
		b, c := test.BaselineExpect, test.CandidateExpect
		if b.ExitCode < 0 || b.ExitCode > 255 || c.ExitCode != 0 {
			return invalid("invalid baseline or candidate exit expectation", nil)
		}
		if b.ExitCode != 0 && b.StdoutContains == "" && b.StderrContains == "" {
			return invalid("nonzero baseline expectation requires literal marker", nil)
		}
		for _, e := range []model.Expectation{b, c} {
			if !utf8.ValidString(e.StdoutContains) || !utf8.ValidString(e.StderrContains) {
				return invalid("invalid expectation UTF-8", nil)
			}
		}
	}
	var cwds [2]string
	for i, v := range []model.Variant{s.Variants.Baseline, s.Variants.Candidate} {
		cwd, err := relativePath(v.CWD)
		if err != nil || cwd == "." {
			return invalid("variant cwd must be below root", err)
		}
		cwds[i] = filepath.ToSlash(cwd)
	}
	// A variant's build may write anywhere below its cwd, so that tree must not
	// hold the other variant's declared inputs, hashed before any build ran.
	if overlapping(cwds[0], cwds[1]) {
		return invalid(fmt.Sprintf("baseline cwd %q and candidate cwd %q overlap; variant directories must differ and neither may contain the other", cwds[0], cwds[1]), nil)
	}
	for _, v := range []model.Variant{s.Variants.Baseline, s.Variants.Candidate} {
		var err error
		if len(v.Build) > model.MaxBuildSteps {
			return invalid("at most 64 build steps per variant", nil)
		}
		for _, cmd := range v.Build {
			if err = argv(cmd.Argv); err != nil {
				return err
			}
			if !timeout(cmd.TimeoutSeconds) {
				return invalid("build timeout must be 1..3600 seconds", nil)
			}
			for key, val := range cmd.Env {
				if !envName.MatchString(key) || strings.ContainsRune(val, 0) || !utf8.ValidString(val) {
					return invalid("invalid command environment", nil)
				}
			}
		}
		if len(v.Inputs) == 0 || len(v.Artifacts) == 0 {
			return invalid("variant inputs and artifacts must be nonempty", nil)
		}
		if err = paths(v.Inputs); err != nil {
			return err
		}
		if err = paths(v.Artifacts); err != nil {
			return err
		}
		artifacts := map[string]bool{}
		for _, p := range v.Artifacts {
			clean, _ := relativePath(p)
			artifacts[clean] = true
		}
		for _, t := range s.Tests {
			clean, _ := relativePath(t.Argv[0])
			if !artifacts[clean] {
				return invalid("test executable is not a declared artifact", nil)
			}
		}
	}
	return paths(s.TestInputs)
}

// overlapping reports whether two cleaned, slash-separated relative directories
// are equal or one contains the other. Whole path components are compared, so
// siblings sharing a name prefix, such as "a" and "ab", do not overlap.
func overlapping(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

func validText(s string) bool { return s != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func timeout(n int) bool      { return n >= model.MinTimeoutSeconds && n <= model.MaxTimeoutSeconds }
func argv(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return invalid("argv requires nonempty executable", nil)
	}
	for _, a := range args {
		if !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
			return invalid("argv contains invalid UTF-8 or NUL", nil)
		}
	}
	return nil
}
func paths(items []string) error {
	seen := map[string]bool{}
	for _, item := range items {
		p, err := relativePath(item)
		if err != nil {
			return invalid("invalid declared path", err)
		}
		if seen[p] {
			return invalid("duplicate normalized declared path", nil)
		}
		seen[p] = true
	}
	return nil
}
func relativePath(p string) (string, error) {
	if !validText(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be nonempty and relative")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("parent path component is forbidden")
		}
	}
	return filepath.Clean(p), nil
}
