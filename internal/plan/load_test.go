// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/plan"
)

func baseSpec() model.Spec {
	v := model.Variant{CWD: "baseline", Build: []model.Command{{Argv: []string{"/bin/true"}, TimeoutSeconds: 1}}, Inputs: []string{"main.c"}, Artifacts: []string{"component"}}
	c := v
	c.CWD = "candidate"
	return model.Spec{SchemaVersion: 1, Target: model.Target{Kind: model.TargetNative}, Variants: model.Variants{Baseline: v, Candidate: c}, TestInputs: []string{"cases.json"}, Tests: []model.Test{{ID: "boundary", Argv: []string{"component", ""}, TimeoutSeconds: 1, BaselineExpect: model.Expectation{ExitCode: 1, StderrContains: "expected assertion"}, CandidateExpect: model.Expectation{ExitCode: 0}}}}
}

func encoded(t *testing.T, s model.Spec) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStrictLoad(t *testing.T) {
	base := string(encoded(t, baseSpec()))
	cases := map[string]string{
		"empty": "", "malformed": "{", "trailing value": base + " {}", "wrong root": "[]",
		"unknown key":           strings.Replace(base, `"schema_version":1`, `"schema_version":1,"extra":true`, 1),
		"case alias":            strings.Replace(base, `"schema_version"`, `"Schema_Version"`, 1),
		"duplicate key":         strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"escaped duplicate key": strings.Replace(base, `"schema_version":1`, `"schema_version":1,"\u0073chema_version":1`, 1),
		"nested duplicate key":  strings.Replace(base, `"argv":`, `"env":{"NAME":"a","NAME":"b"},"argv":`, 1),
		"null array":            strings.Replace(base, `"test_inputs":["cases.json"]`, `"test_inputs":null`, 1),
		"null array entry":      strings.Replace(base, `"inputs":["main.c"]`, `"inputs":[null]`, 1),
		"missing exit code":     strings.Replace(base, `"candidate_expect":{"exit_code":0}`, `"candidate_expect":{}`, 1),
		"null exit code":        strings.Replace(base, `"candidate_expect":{"exit_code":0}`, `"candidate_expect":{"exit_code":null}`, 1),
		"empty supplied marker": strings.Replace(base, `"candidate_expect":{"exit_code":0}`, `"candidate_expect":{"exit_code":0,"stdout_contains":""}`, 1),
		"env null value":        strings.Replace(base, `"argv":`, `"env":{"NAME":null},"argv":`, 1),
		"invalid UTF8":          base + string([]byte{0xff}),
		"oversized":             strings.Repeat(" ", model.MaxManifestBytes+1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := plan.Load(strings.NewReader(data))
			var typed *model.RunError
			if !errors.As(err, &typed) || typed.Kind != model.ErrorInvalidInput {
				t.Fatalf("expected typed input error, got %v", err)
			}
		})
	}
	loaded, err := plan.Load(strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.ManifestJSON, []byte(base)) {
		t.Fatal("exact manifest bytes lost")
	}
	if loaded.Tests[0].Argv[1] != "" {
		t.Fatal("empty non-executable argument lost")
	}
	padded := base + strings.Repeat(" ", model.MaxManifestBytes-len(base))
	if _, err = plan.Load(strings.NewReader(padded)); err != nil {
		t.Fatalf("exact byte limit refused: %v", err)
	}
}

func TestSemanticLoadRejections(t *testing.T) {
	cases := map[string]func(*model.Spec){
		"unsupported version":       func(s *model.Spec) { s.SchemaVersion = 2 },
		"unknown target":            func(s *model.Spec) { s.Target.Kind = "arm64" },
		"native qemu fields":        func(s *model.Spec) { s.Target.Executable = "qemu-arm" },
		"missing qemu config":       func(s *model.Spec) { s.Target.Kind = model.TargetQEMUARM },
		"duplicate IDs":             func(s *model.Spec) { s.Tests = append(s.Tests, s.Tests[0]) },
		"empty argv":                func(s *model.Spec) { s.Tests[0].Argv = nil },
		"empty executable":          func(s *model.Spec) { s.Tests[0].Argv[0] = "" },
		"NUL argv":                  func(s *model.Spec) { s.Tests[0].Argv[0] = "component\x00" },
		"nonzero without marker":    func(s *model.Spec) { s.Tests[0].BaselineExpect.StderrContains = "" },
		"negative exit":             func(s *model.Spec) { s.Tests[0].BaselineExpect.ExitCode = -1 },
		"candidate failure":         func(s *model.Spec) { s.Tests[0].CandidateExpect.ExitCode = 1 },
		"zero timeout":              func(s *model.Spec) { s.Tests[0].TimeoutSeconds = 0 },
		"excess timeout":            func(s *model.Spec) { s.Variants.Baseline.Build[0].TimeoutSeconds = 3601 },
		"same cwd":                  func(s *model.Spec) { s.Variants.Candidate.CWD = "./baseline" },
		"root cwd":                  func(s *model.Spec) { s.Variants.Candidate.CWD = "." },
		"parent cwd":                func(s *model.Spec) { s.Variants.Candidate.CWD = "../candidate" },
		"absolute input":            func(s *model.Spec) { s.Variants.Baseline.Inputs = []string{"/etc/passwd"} },
		"parent input":              func(s *model.Spec) { s.TestInputs = []string{"baseline/../cases.json"} },
		"missing declarations":      func(s *model.Spec) { s.Variants.Baseline.Inputs = nil },
		"undeclared executable":     func(s *model.Spec) { s.Tests[0].Argv[0] = "different" },
		"duplicate normalized path": func(s *model.Spec) { s.Variants.Baseline.Artifacts = []string{"component", "./component"} },
		"bad env name":              func(s *model.Spec) { s.Variants.Baseline.Build[0].Env = map[string]string{"BAD=NAME": "value"} },
		"NUL env":                   func(s *model.Spec) { s.Variants.Baseline.Build[0].Env = map[string]string{"NAME": "\x00"} },
		"no tests":                  func(s *model.Spec) { s.Tests = nil },
		"test limit": func(s *model.Spec) {
			s.Tests = nil
			for i := 0; i < 257; i++ {
				test := baseSpec().Tests[0]
				test.ID = fmt.Sprintf("test-%d", i)
				s.Tests = append(s.Tests, test)
			}
		},
		"build limit": func(s *model.Spec) {
			for len(s.Variants.Baseline.Build) < 65 {
				s.Variants.Baseline.Build = append(s.Variants.Baseline.Build, model.Command{Argv: []string{"/bin/true"}, TimeoutSeconds: 1})
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			mutate(&s)
			if _, err := plan.Load(bytes.NewReader(encoded(t, s))); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

// A variant's build may write anywhere below its cwd, so neither variant
// directory may equal or contain the other. Load rejects the manifest before
// any filesystem inspection or command.
func TestVariantWorkspacesMustNotOverlap(t *testing.T) {
	overlap := func(baseline, candidate string) string {
		return fmt.Sprintf("baseline cwd %q and candidate cwd %q overlap; variant directories must differ and neither may contain the other", baseline, candidate)
	}
	for _, c := range []struct {
		name, baseline, candidate, message string
	}{
		{"equal", "work", "work", overlap("work", "work")},
		{"equal after normalization", "work", "./work/", overlap("work", "work")},
		{"candidate inside baseline", "work", "work/candidate", overlap("work", "work/candidate")},
		{"baseline inside candidate", "work/variants/baseline", "work", overlap("work/variants/baseline", "work")},
		{"root and subdirectory", ".", "candidate", "variant cwd must be below root"},
		{"subdirectory and root", "baseline", "./", "variant cwd must be below root"},
		{"siblings sharing a name prefix", "a", "ab", ""},
		{"nested siblings sharing a name prefix", "rows/a", "rows/ab", ""},
		{"prefix with a trailing separator", "rows/a/", "rows/ab", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := baseSpec()
			s.Variants.Baseline.CWD, s.Variants.Candidate.CWD = c.baseline, c.candidate
			_, err := plan.Load(bytes.NewReader(encoded(t, s)))
			if c.message == "" {
				if err != nil {
					t.Fatalf("non-overlapping variant directories rejected: %v", err)
				}
				return
			}
			var typed *model.RunError
			if !errors.As(err, &typed) || typed.Kind != model.ErrorInvalidInput || typed.Failure.Kind != model.FailureInvalidInput || typed.Failure.Message != c.message {
				t.Fatalf("got %v, want invalid_input %q", err, c.message)
			}
		})
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestReaderFailures(t *testing.T) {
	for _, r := range []io.Reader{nil, brokenReader{}} {
		if _, err := plan.Load(r); err == nil {
			t.Fatal("reader failure accepted")
		}
	}
}

func TestManifestFixtureCatalog(t *testing.T) {
	var entries []struct {
		File, Schema string
		Valid        bool
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Schema != "manifest-v1.json" {
			continue
		}
		t.Run(entry.File, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", entry.File))
			if err != nil {
				t.Fatal(err)
			}
			_, err = plan.Load(bytes.NewReader(data))
			if (err == nil) != entry.Valid {
				t.Fatalf("valid=%v, Load error %v", entry.Valid, err)
			}
		})
	}
}
