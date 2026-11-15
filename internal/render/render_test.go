// SPDX-License-Identifier: Apache-2.0

package render_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/render"
)

var update = flag.Bool("update", false, "rewrite explicitly requested report goldens")

func ptr(n int) *int { return &n }
func fixture(name string) model.Report {
	r := model.Report{SchemaVersion: 1, Tool: model.ToolVersion{Version: "0.1.0-test", Commit: "fixture", GoVersion: "go1.27.4"}, Target: model.TargetInfo{Kind: model.TargetNative, HostOS: "linux", HostArch: "amd64"}, Manifest: &model.ManifestIdentity{SHA256: strings.Repeat("0", 64), Encoding: "raw_json"}, Outcome: model.OutcomeMatched,
		Inputs: []model.FileIdentity{{Path: "z-test.c", SHA256: strings.Repeat("1", 64), SizeBytes: 20}, {Path: "a-config.h", SHA256: strings.Repeat("2", 64), SizeBytes: 10}},
		Tools:  []model.ToolIdentity{{Role: "compiler", Path: "/usr/bin/gcc", Version: "gcc 12.2.0"}}}
	for _, variant := range []model.VariantName{model.Baseline, model.Candidate} {
		cwd := string(variant)
		artifact := model.ArtifactIdentity{FileIdentity: model.FileIdentity{Path: cwd + "/parser", SHA256: strings.Repeat("3", 64), SizeBytes: 100}, ELF: model.ELFIdentity{Class: "ELF64", Machine: "EM_X86_64", Endian: "little", ABI: "linux-amd64"}}
		v := model.VariantReport{Name: variant, CWD: cwd, ArtifactsBefore: []model.ArtifactIdentity{artifact}, ArtifactsAfter: []model.ArtifactIdentity{artifact}}
		makeCheck := func(id string, index int, kind model.CheckKind, argv []string, expected model.Expectation) model.Check {
			log := fmt.Sprintf("logs/%s-%s-%d", variant, kind, index)
			return model.Check{ID: id, Index: index, Kind: kind, Argv: argv, CWD: cwd, EnvNames: []string{"CFLAGS"}, TimeoutSeconds: 5, Expectation: expected, Status: model.CheckMatched, Reason: "normal exit and all declared markers matched", Observation: &model.ProcessObservation{Started: true, Argv: append([]string{}, argv...), CWD: cwd, EnvNames: []string{"CFLAGS"}, ExitCode: ptr(expected.ExitCode), Stdout: model.Capture{Log: log + ".stdout", Data: []byte("RAW-SECRET-NOT-IN-REPORT")}, Stderr: model.Capture{Log: log + ".stderr"}, DurationMillis: 12}}
		}
		v.Build = []model.Check{makeCheck("build-0", 0, model.CheckBuild, []string{"gcc", "test.c", "-o", "parser"}, model.Expectation{ExitCode: 0})}
		for i, id := range []string{"security", "compatibility"} {
			expected := model.Expectation{ExitCode: 0}
			if variant == model.Baseline && i == 0 {
				expected.ExitCode = 3
				expected.StderrContains = "signed overflow"
			}
			c := makeCheck(id, i, model.CheckTest, []string{"./parser", id}, expected)
			if expected.ExitCode == 3 {
				c.Observation.Stderr.Bytes = 15
			}
			v.Tests = append(v.Tests, c)
		}
		r.Variants = append(r.Variants, v)
		r.Declared = append(r.Declared, model.VariantCoverage{Name: variant, BuildIDs: []string{"build-0"}, TestIDs: []string{"security", "compatibility"}})
	}
	notRun := func(c *model.Check) {
		c.Status = model.CheckNotRun
		c.Reason = "not run after infrastructure failure"
		c.Observation = nil
	}
	stopCandidate := func() {
		for i := range r.Variants[1].Build {
			notRun(&r.Variants[1].Build[i])
		}
		for i := range r.Variants[1].Tests {
			notRun(&r.Variants[1].Tests[i])
		}
		r.Variants[1].ArtifactsBefore = []model.ArtifactIdentity{}
		r.Variants[1].ArtifactsAfter = []model.ArtifactIdentity{}
	}
	switch name {
	case "matched-negative":
	case "candidate-mismatch":
		r.Outcome = model.OutcomeMismatch
		c := &r.Variants[1].Tests[0]
		c.Status = model.CheckMismatch
		c.Reason = "normal exit 1 did not match expected exit 0"
		c.Observation.ExitCode = ptr(1)
	case "build-failure":
		r.Outcome = model.OutcomeError
		r.Failure = &model.Failure{Kind: model.FailureBuild, Message: "baseline build exited 2"}
		c := &r.Variants[0].Build[0]
		c.Status = model.CheckError
		c.Reason = "build failed"
		c.Observation.ExitCode = ptr(2)
		for i := range r.Variants[0].Tests {
			notRun(&r.Variants[0].Tests[i])
		}
		r.Variants[0].ArtifactsBefore = []model.ArtifactIdentity{}
		r.Variants[0].ArtifactsAfter = []model.ArtifactIdentity{}
		stopCandidate()
	case "timeout":
		r.Outcome = model.OutcomeError
		r.Failure = &model.Failure{Kind: model.FailureTimeout, Message: "candidate security exceeded 5 seconds"}
		c := &r.Variants[1].Tests[0]
		c.Status = model.CheckError
		c.Reason = "timeout is infrastructure failure"
		c.Observation.ExitCode = nil
		c.Observation.Signal = "killed"
		c.Observation.Failure = r.Failure
		c.Observation.DurationMillis = 5250
		notRun(&r.Variants[1].Tests[1])
	case "cancelled-partial":
		r.Outcome = model.OutcomeCancelled
		r.Failure = &model.Failure{Kind: model.FailureCancelled, Message: "caller cancelled during baseline compatibility"}
		c := &r.Variants[0].Tests[1]
		c.Status = model.CheckError
		c.Reason = "caller cancellation"
		c.Observation.ExitCode = nil
		c.Observation.Signal = "terminated"
		c.Observation.Failure = r.Failure
		stopCandidate()
	case "identity-change":
		r.Outcome = model.OutcomeError
		r.Failure = &model.Failure{Kind: model.FailureIdentity, Message: "candidate/parser changed during testing"}
		r.Variants[1].ArtifactsAfter[0].SHA256 = strings.Repeat("4", 64)
		for i := range r.Variants[1].Tests {
			r.Variants[1].Tests[i].Status = model.CheckError
			r.Variants[1].Tests[i].Reason = "artifact identity changed; behavioral verdict unavailable"
		}
	default:
		panic(name)
	}
	return r
}
func outputs(t *testing.T, r model.Report) ([]byte, []byte) {
	t.Helper()
	var j, m bytes.Buffer
	if err := render.WriteJSON(&j, r); err != nil {
		t.Fatal(err)
	}
	if err := render.WriteMarkdown(&m, r); err != nil {
		t.Fatal(err)
	}
	return j.Bytes(), m.Bytes()
}

func TestGoldenStatesAndRoundTrip(t *testing.T) {
	if *update {
		if err := os.MkdirAll("testdata", 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"matched-negative", "candidate-mismatch", "build-failure", "timeout", "cancelled-partial", "identity-change"} {
		t.Run(name, func(t *testing.T) {
			r := fixture(name)
			j, m := outputs(t, r)
			for suffix, data := range map[string][]byte{"json": j, "md": m} {
				path := filepath.Join("testdata", name+"."+suffix)
				if *update {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, want) {
					t.Fatalf("%s golden differs", suffix)
				}
			}
			var back model.Report
			if err := json.Unmarshal(j, &back); err != nil {
				t.Fatal(err)
			}
			if back.Outcome != r.Outcome || back.SchemaVersion != 1 || !reflect.DeepEqual(back.Declared, r.Declared) {
				t.Fatal("aggregate/version/coverage changed on JSON round trip")
			}
			visible := html.UnescapeString(string(m))
			for vi, v := range r.Variants {
				for _, list := range []struct{ original, back []model.Check }{{v.Build, back.Variants[vi].Build}, {v.Tests, back.Variants[vi].Tests}} {
					for i, c := range list.original {
						b := list.back[i]
						if b.ID != c.ID || b.Status != c.Status || b.Reason != c.Reason || !reflect.DeepEqual(b.Expectation, c.Expectation) {
							t.Fatal("check evidence changed")
						}
						if !strings.Contains(visible, fmt.Sprintf("status: %q", c.Status)) || !strings.Contains(visible, fmt.Sprintf("Reason: %q", c.Reason)) {
							t.Fatal("Markdown omitted exact check state/reason")
						}
						if c.Observation == nil {
							if b.Observation != nil {
								t.Fatal("not_run gained an observation")
							}
						} else if !reflect.DeepEqual(b.Observation.ExitCode, c.Observation.ExitCode) || !reflect.DeepEqual(b.Observation.Failure, c.Observation.Failure) {
							t.Fatal("actual normal exit/failure lost")
						}
					}
				}
			}
			if bytes.Contains(j, []byte("RAW-SECRET")) || bytes.Contains(m, []byte("RAW-SECRET")) {
				t.Fatal("raw capture serialized")
			}
		})
	}
}
func TestStableOrderingWithoutMutationAndConcurrentWriters(t *testing.T) {
	r := fixture("matched-negative")
	before, _ := json.Marshal(r)
	wantJ, wantM := outputs(t, r)
	if strings.Index(string(wantJ), "a-config.h") > strings.Index(string(wantJ), "z-test.c") {
		t.Fatal("input digests not sorted")
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Fatal("renderer mutated input")
	}
	r.Variants[0], r.Variants[1] = r.Variants[1], r.Variants[0]
	r.Declared[0], r.Declared[1] = r.Declared[1], r.Declared[0]
	for i := range r.Variants {
		v := &r.Variants[i]
		v.Tests[0], v.Tests[1] = v.Tests[1], v.Tests[0]
	}
	for i := 0; i < 20; i++ {
		j, m := outputs(t, r)
		if !bytes.Equal(j, wantJ) || !bytes.Equal(m, wantM) {
			t.Fatal("declared order not normalized stably")
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var j, m bytes.Buffer
			if err := render.WriteJSON(&j, r); err != nil {
				t.Error(err)
			}
			if err := render.WriteMarkdown(&m, r); err != nil {
				t.Error(err)
			}
			if !bytes.Equal(j.Bytes(), wantJ) || !bytes.Equal(m.Bytes(), wantM) {
				t.Error("concurrent render differs")
			}
		}()
	}
	wg.Wait()
}
func TestArtifactToolAndEnvironmentOrderingAndUnicode(t *testing.T) {
	r := fixture("matched-negative")
	r.Tools = append(r.Tools, model.ToolIdentity{Role: "assembler", Path: "/usr/bin/as", VersionError: "version flag unavailable"})
	for i := range r.Variants {
		v := &r.Variants[i]
		a := v.ArtifactsBefore[0]
		a.Path = string(v.Name) + "/a-artifact"
		v.ArtifactsBefore = append(v.ArtifactsBefore, a)
		v.ArtifactsAfter = append(v.ArtifactsAfter, a)
		v.Tests[0].EnvNames = []string{"Z_NAME", "A_NAME"}
		v.Tests[0].Observation.EnvNames = []string{"Z_NAME", "A_NAME"}
		v.Tests[0].Observation.Stdout.Data = []byte{0xff, 0x00}
	}
	r.Tool.Version = "版本 <test> \u2028"
	before, _ := json.Marshal(r)
	j, m := outputs(t, r)
	var back model.Report
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tool.Version != r.Tool.Version {
		t.Fatal("Unicode changed")
	}
	if back.Tools[0].Role != "assembler" {
		t.Fatal("tool order unstable")
	}
	for _, v := range back.Variants {
		if !strings.HasSuffix(v.ArtifactsBefore[0].Path, "/a-artifact") || !strings.HasSuffix(v.ArtifactsAfter[0].Path, "/a-artifact") {
			t.Fatal("artifact paths not sorted")
		}
		if !reflect.DeepEqual(v.Tests[0].EnvNames, []string{"A_NAME", "Z_NAME"}) || !reflect.DeepEqual(v.Tests[0].Observation.EnvNames, []string{"A_NAME", "Z_NAME"}) {
			t.Fatal("override names not sorted")
		}
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Fatal("sorted backing arrays mutated")
	}
	if bytes.Contains(m, []byte("<test>")) {
		t.Fatal("HTML-like Unicode text unescaped")
	}
}

func TestEscapingKeepsDynamicTextOutOfMarkdownStructure(t *testing.T) {
	r := fixture("matched-negative")
	_, benign := outputs(t, r)
	malicious := "x\n# injected\n<script>alert(1)</script> [a](javascript:alert(1)) ``` ` | * _ & \\ end"
	for i := range r.Declared {
		r.Declared[i].TestIDs[0] = malicious
		r.Variants[i].Tests[0].ID = malicious
		r.Variants[i].Tests[0].Reason = malicious
		r.Variants[i].Tests[0].Argv[1] = malicious
		r.Variants[i].Tests[0].Observation.Argv[1] = malicious
	}
	r.Tool.Version = malicious
	r.Inputs[0].Path = "file with spaces\n## heading <img>.c"
	j, m := outputs(t, r)
	if bytes.Count(m, []byte{'\n'}) != bytes.Count(benign, []byte{'\n'}) {
		t.Fatal("dynamic newline changed Markdown structure")
	}
	for _, bad := range []string{"<script>", "<img>", "javascript:", "```", "\n# injected", "[a]("} {
		if bytes.Contains(m, []byte(bad)) {
			t.Fatalf("unescaped dynamic syntax %q", bad)
		}
	}
	var back model.Report
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tool.Version != malicious || back.Variants[0].Tests[0].ID != malicious || back.Inputs[1].Path != r.Inputs[0].Path {
		t.Fatal("JSON altered exact malicious strings")
	}
	if !strings.Contains(html.UnescapeString(string(m)), `\n# injected`) {
		t.Fatal("Markdown failed to expose newline as quoted text")
	}
}
func TestUnsafeLogReferencesFailBeforeWriting(t *testing.T) {
	for _, path := range []string{"../escape.stdout", "/logs/a.stdout", "logs/a b.stdout", "logs/a.stdout\n#evil", "logs/[x](javascript:bad).stdout", "logs/%2e%2e.stdout", "https://evil/a.stdout", "logs/a\\b.stdout", "logs/a.stderr"} {
		t.Run(path, func(t *testing.T) {
			r := fixture("matched-negative")
			r.Variants[0].Tests[0].Observation.Stdout.Log = path
			for _, writer := range []func(io.Writer, model.Report) error{render.WriteJSON, render.WriteMarkdown} {
				var b bytes.Buffer
				if err := writer(&b, r); err == nil || b.Len() != 0 {
					t.Fatal("unsafe log not rejected before output")
				}
			}
		})
	}
}
func TestErrorDetailsAndSafeLogLinks(t *testing.T) {
	r := fixture("timeout")
	o := r.Variants[1].Tests[0].Observation
	o.Failure = &model.Failure{Kind: model.FailureOutputLimit, Message: "stdout exceeded 1 MiB"}
	o.Stdout.Truncated = true
	o.Stdout.Bytes = model.MaxCaptureBytes + 17
	j, m := outputs(t, r)
	for _, value := range []string{"output_limit", "killed", "1048593", "true"} {
		if !bytes.Contains(j, []byte(value)) || !strings.Contains(html.UnescapeString(string(m)), value) {
			t.Fatalf("missing observed detail %s", value)
		}
	}
	if !bytes.Contains(m, []byte("[stdout log](logs/candidate-test-0.stdout)")) {
		t.Fatal("safe relative link missing")
	}
}
func TestEmptyErrorDoesNotGainSuccessAndMalformedModelsFail(t *testing.T) {
	r := fixture("build-failure")
	r.Variants = nil
	r.Declared = nil
	r.Inputs = nil
	r.Tools = nil
	r.Manifest = nil
	j, m := outputs(t, r)
	if !bytes.Contains(j, []byte(`"variants": []`)) || !bytes.Contains(m, []byte("No variant observations.")) || bytes.Contains(m, []byte("matched")) {
		t.Fatal("empty error report invented evidence")
	}
	for _, mutate := range []func(*model.Report){func(r *model.Report) { r.SchemaVersion = 9 }, func(r *model.Report) { r.Outcome = "passed" }, func(r *model.Report) { r.Declared = nil }, func(r *model.Report) { r.Variants[0].Tests[0].Status = model.CheckError }, func(r *model.Report) { r.Variants[0].Tests[0].Observation = nil }, func(r *model.Report) { r.Tool.Version = string([]byte{0xff}) }} {
		r := fixture("matched-negative")
		mutate(&r)
		for _, writer := range []func(io.Writer, model.Report) error{render.WriteJSON, render.WriteMarkdown} {
			var b bytes.Buffer
			if err := writer(&b, r); err == nil || b.Len() != 0 {
				t.Fatal("malformed model rendered")
			}
		}
	}
}

var errSink = errors.New("sink stopped")

type failedWriter struct{}

func (failedWriter) Write(p []byte) (int, error) { return min(17, len(p)), errSink }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return min(17, len(p)), nil }
func TestWriterErrorsPropagate(t *testing.T) {
	for _, writer := range []func(io.Writer, model.Report) error{render.WriteJSON, render.WriteMarkdown} {
		if err := writer(failedWriter{}, fixture("matched-negative")); !errors.Is(err, errSink) {
			t.Fatalf("writer error lost: %v", err)
		}
		if err := writer(shortWriter{}, fixture("matched-negative")); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short write lost: %v", err)
		}
		if err := writer(nil, fixture("matched-negative")); err == nil {
			t.Fatal("nil writer accepted")
		}
	}
}

func TestSchemaRepresentationGuards(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.Report)
	}{
		{"manifest missing", func(r *model.Report) { r.Manifest = nil }},
		{"inputs missing", func(r *model.Report) { r.Inputs = nil }},
		{"artifacts before missing", func(r *model.Report) { r.Variants[0].ArtifactsBefore = nil }},
		{"artifacts after missing", func(r *model.Report) { r.Variants[1].ArtifactsAfter = nil }},
		{"tool version missing", func(r *model.Report) { r.Tool.Version = "" }},
		{"target metadata missing", func(r *model.Report) { r.Target.HostArch = "" }},
		{"target unsupported", func(r *model.Report) { r.Target.Kind = "other" }},
		{"manifest digest", func(r *model.Report) { r.Manifest.SHA256 = "bad" }},
		{"manifest encoding", func(r *model.Report) { r.Manifest.Encoding = "unknown" }},
		{"file path escape", func(r *model.Report) { r.Inputs[0].Path = "../outside" }},
		{"file digest", func(r *model.Report) { r.Inputs[0].SHA256 = "bad" }},
		{"file size", func(r *model.Report) { r.Inputs[0].SizeBytes = -1 }},
		{"ELF fields", func(r *model.Report) { r.Variants[0].ArtifactsBefore[0].ELF.ABI = "unknown" }},
		{"tool version conflict", func(r *model.Report) { r.Tools[0].VersionError = "unknown" }},
		{"mismatching build in completed report", func(r *model.Report) {
			r.Outcome = model.OutcomeMismatch
			r.Variants[0].Build[0].Status = model.CheckMismatch
		}},
		{"kind", func(r *model.Report) { r.Variants[0].Tests[0].Kind = "unknown" }},
		{"index", func(r *model.Report) { r.Variants[0].Tests[0].Index = -1 }},
		{"timeout", func(r *model.Report) { r.Variants[0].Tests[0].TimeoutSeconds = 3601 }},
		{"empty argv", func(r *model.Report) { r.Variants[0].Tests[0].Argv = nil }},
		{"cwd", func(r *model.Report) { r.Variants[0].Tests[0].CWD = "/outside" }},
		{"planned env value", func(r *model.Report) { r.Variants[0].Tests[0].EnvNames = []string{"SECRET=value"} }},
		{"observed env value", func(r *model.Report) { r.Variants[0].Tests[0].Observation.EnvNames = []string{"SECRET=value"} }},
		{"capture count", func(r *model.Report) { r.Variants[0].Tests[0].Observation.Stdout.Bytes = -1 }},
		{"duration", func(r *model.Report) { r.Variants[0].Tests[0].Observation.DurationMillis = -1 }},
		{"exit range", func(r *model.Report) { r.Variants[0].Tests[0].Observation.ExitCode = ptr(256) }},
		{"failure kind", func(r *model.Report) {
			r.Outcome = model.OutcomeError
			r.Failure = &model.Failure{Kind: "unknown", Message: "error"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture("matched-negative")
			tc.mutate(&r)
			for _, writer := range []func(io.Writer, model.Report) error{render.WriteJSON, render.WriteMarkdown} {
				var b bytes.Buffer
				if err := writer(&b, r); err == nil || b.Len() != 0 {
					t.Fatal("invalid representation written")
				}
			}
		})
	}
}
func TestPartialTargetFieldsArePreservedIndependently(t *testing.T) {
	r := fixture("build-failure")
	r.Target = model.TargetInfo{Kind: model.TargetQEMUARM, HostOS: "linux", HostArch: "arm64", Sysroot: "/known sysroot <path>"}
	j, m := outputs(t, r)
	var back model.Report
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatal(err)
	}
	if back.Target.Sysroot != r.Target.Sysroot || !strings.Contains(html.UnescapeString(string(m)), fmt.Sprintf("Sysroot: %q", r.Target.Sysroot)) {
		t.Fatal("known partial sysroot dropped")
	}
	if bytes.Contains(m, []byte("QEMU executable:")) {
		t.Fatal("missing emulator invented")
	}
	r.Target.Executable = "/known-qemu"
	r.Target.Sysroot = ""
	_, m = outputs(t, r)
	if !strings.Contains(html.UnescapeString(string(m)), `QEMU executable: "/known-qemu"`) {
		t.Fatal("known emulator dropped")
	}
}
func TestRendererDoesNotReevaluateExpectations(t *testing.T) {
	r := fixture("matched-negative")
	r.Variants[0].Tests[0].Observation.ExitCode = ptr(126)
	j, _ := outputs(t, r)
	var back model.Report
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatal(err)
	}
	if back.Outcome != r.Outcome || back.Variants[0].Tests[0].Status != r.Variants[0].Tests[0].Status || *back.Variants[0].Tests[0].Observation.ExitCode != 126 {
		t.Fatal("renderer recomputed status or hid actual exit")
	}
}
