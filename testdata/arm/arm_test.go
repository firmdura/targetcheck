//go:build linux

// SPDX-License-Identifier: Apache-2.0

// Package armtest is make test-arm: actual cross-built ARM32 hard-float
// binaries executed by real qemu-arm -L SYSROOT through the TargetCheck CLI.
// A missing emulator, cross compiler or sysroot fails the run; nothing is
// skipped or mocked. The component is synthetic tool-integration evidence;
// the real Wget case is make demo.
package armtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	tc "github.com/firmdura/targetcheck/pkg/targetcheck"
)

const sysroot = "/usr/arm-linux-gnueabihf"

var (
	cli      string
	repoRoot string
	armELF   = model.ELFIdentity{Class: "ELF32", Machine: "EM_ARM", Endian: "little", ABI: "arm-eabi5-hard-float"}
)

func TestMain(m *testing.M) {
	var missing []string
	for _, tool := range []string{"qemu-arm", "arm-linux-gnueabihf-gcc", "gcc", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	for _, file := range []string{"lib/ld-linux-armhf.so.3", "lib/libc.so.6", "lib/libc.a"} {
		if info, err := os.Stat(filepath.Join(sysroot, file)); err != nil || !info.Mode().IsRegular() {
			missing = append(missing, filepath.Join(sysroot, file))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "test-arm: FAIL: missing %s. Install qemu-user, gcc-arm-linux-gnueabihf and libc6-dev-armhf-cross, or use the pinned container (make container-build demo-container). This is a failure, not a skip.\n", strings.Join(missing, ", "))
		os.Exit(2)
	}
	var err error
	if repoRoot, err = filepath.Abs("../.."); err != nil {
		panic(err)
	}
	tmp, err := os.MkdirTemp("", "targetcheck-arm-")
	if err != nil {
		panic(err)
	}
	cli = filepath.Join(tmp, "targetcheck")
	// Without VCS stamping the build also works in a bind-mounted checkout
	// that git does not trust; these tests do not inspect the tool commit.
	build := exec.Command("go", "build", "-buildvcs=false", "-o", cli, "./cmd/targetcheck")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "test-arm: build CLI: %s: %v\n", out, err)
		os.RemoveAll(tmp)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

type result struct {
	exit           int
	report         model.Report
	out            string
	stdout, stderr string
}

// workspace creates a fresh Root with both variant directories and an empty
// directory that relative-sysroot manifests use.
func workspace(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	source, err := os.ReadFile("fixture/component.c")
	must(t, err)
	for _, dir := range []string{"baseline", "candidate"} {
		must(t, os.Mkdir(filepath.Join(root, dir), 0o700))
		must(t, os.WriteFile(filepath.Join(root, dir, "component.c"), source, 0o600))
	}
	must(t, os.Mkdir(filepath.Join(root, "empty-sysroot"), 0o700))
	return root
}

func check(t *testing.T, manifest, root string) result {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	out := filepath.Join(parent, "out")
	cmd := exec.Command(cli, "check", "--manifest", filepath.Join("manifests", manifest+".json"), "--root", root, "--output", out)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	r := result{out: out, stdout: stdout.String(), stderr: stderr.String()}
	if exitErr, ok := err.(*exec.ExitError); ok {
		r.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run CLI: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "report.json"))
	must(t, err)
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	must(t, d.Decode(&r.report))
	if _, err := os.Stat(filepath.Join(out, "INCOMPLETE")); err == nil {
		t.Fatalf("%s: report output is INCOMPLETE", manifest)
	}
	t.Logf("%s: CLI exit %d, outcome %s", manifest, r.exit, r.report.Outcome)
	return r
}

func (r result) log(t *testing.T, c model.Capture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.out, c.Log))
	must(t, err)
	if int64(len(data)) != c.Bytes || c.Truncated {
		t.Fatalf("log %s has %d bytes, report records %d (truncated=%v)", c.Log, len(data), c.Bytes, c.Truncated)
	}
	return string(data)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// assertQEMUVersion requires the version that the run recorded, by querying the
// configured emulator once after preflight, to be the first line that the real
// qemu-arm --version prints.
func assertQEMUVersion(t *testing.T, rep model.Report) {
	t.Helper()
	out, err := exec.Command(rep.Target.Executable, "--version").Output()
	must(t, err)
	want := ""
	for _, line := range strings.Split(string(out), "\n") {
		if want = strings.TrimSpace(line); want != "" {
			break
		}
	}
	if !strings.HasPrefix(want, "qemu-arm version ") {
		t.Fatalf("%s --version printed %q", rep.Target.Executable, out)
	}
	var found []model.ToolIdentity
	for _, tool := range rep.Tools {
		if tool.Role == "qemu" {
			found = append(found, tool)
		}
	}
	if len(found) != 1 || found[0].Path != rep.Target.Executable || found[0].Version != want || found[0].VersionError != "" {
		t.Fatalf("qemu tool observations %+v, want path %s and version %q", found, rep.Target.Executable, want)
	}
}

// assertARMRun checks real QEMU execution evidence for every observed test.
func assertARMRun(t *testing.T, r result, wantSysroot string) {
	t.Helper()
	rep := r.report
	if rep.Target.Kind != model.TargetQEMUARM || !strings.HasPrefix(filepath.Base(rep.Target.Executable), "qemu-arm") || rep.Target.Sysroot != wantSysroot {
		t.Fatalf("target %+v, want qemu-arm with sysroot %s", rep.Target, wantSysroot)
	}
	assertQEMUVersion(t, rep)
	if len(rep.Variants) != 2 {
		t.Fatalf("variants %d, want 2", len(rep.Variants))
	}
	for _, v := range rep.Variants {
		for _, b := range v.Build {
			if b.Status != model.CheckMatched {
				t.Fatalf("%s build %s: %s %s", v.Name, b.ID, b.Status, b.Reason)
			}
		}
		if len(v.ArtifactsAfter) != 1 || v.ArtifactsAfter[0].ELF != armELF || !reflect.DeepEqual(v.ArtifactsBefore, v.ArtifactsAfter) {
			t.Fatalf("%s artifacts before %+v after %+v, want one unchanged %+v", v.Name, v.ArtifactsBefore, v.ArtifactsAfter, armELF)
		}
		for _, test := range v.Tests {
			o := test.Observation
			if o == nil || !o.Started || o.ExitCode == nil {
				t.Fatalf("%s test %s was not executed: %s", v.Name, test.ID, test.Reason)
			}
			want := []string{rep.Target.Executable, "-L", wantSysroot, "./component", test.ID}
			if !slices.Equal(o.Argv, want) {
				t.Fatalf("%s test %s argv %q, want %q", v.Name, test.ID, o.Argv, want)
			}
			if test.ID == "abi" && !strings.Contains(r.log(t, o.Stdout), "ARM32 EABI hard-float: long=32 bits, long long=64 bits, scale=4.5") {
				t.Fatalf("%s abi output does not show ARM32 hard-float execution", v.Name)
			}
		}
	}
}

func statuses(rep model.Report) [][]model.CheckStatus {
	var all [][]model.CheckStatus
	for _, v := range rep.Variants {
		var s []model.CheckStatus
		for _, test := range v.Tests {
			s = append(s, test.Status)
		}
		all = append(all, s)
	}
	return all
}

func TestMatchedARMComparison(t *testing.T) {
	root := workspace(t)
	r := check(t, "matched", root)
	if r.exit != 0 || r.report.Outcome != model.OutcomeMatched || r.report.Failure != nil {
		t.Fatalf("exit %d outcome %s failure %+v\n%s", r.exit, r.report.Outcome, r.report.Failure, r.stderr)
	}
	assertARMRun(t, r, sysroot)
	baseline := r.report.Variants[0].Tests[1]
	if *baseline.Observation.ExitCode != 1 || !strings.Contains(r.log(t, baseline.Observation.Stderr), `large-offset assertion failed: strtol("2147483648") rejected a 64-bit offset (errno=ERANGE)`) {
		t.Fatalf("baseline large-offset did not show the 32-bit strtol limit: %+v", baseline)
	}
	candidate := r.report.Variants[1].Tests[1]
	if !strings.Contains(r.log(t, candidate.Observation.Stdout), `large-offset ok: strtoll("2147483648") = 2147483648`) {
		t.Fatalf("candidate large-offset output is missing")
	}

	// The public library reaches the same verdicts and identities.
	f, err := os.Open("manifests/matched.json")
	must(t, err)
	defer f.Close()
	spec, err := tc.Load(f)
	must(t, err)
	library, err := tc.Run(context.Background(), spec, tc.Options{Root: workspace(t)})
	must(t, err)
	if library.Outcome != r.report.Outcome || !reflect.DeepEqual(statuses(library), statuses(r.report)) {
		t.Fatalf("library outcome %s %v, CLI %s %v", library.Outcome, statuses(library), r.report.Outcome, statuses(r.report))
	}
	assertQEMUVersion(t, library)
	for i := range library.Variants {
		if !reflect.DeepEqual(library.Variants[i].ArtifactsAfter[0].ELF, r.report.Variants[i].ArtifactsAfter[0].ELF) || library.Variants[i].ArtifactsAfter[0].SHA256 != r.report.Variants[i].ArtifactsAfter[0].SHA256 {
			t.Fatalf("library and CLI artifacts differ for %s", library.Variants[i].Name)
		}
	}
}

func TestCandidateRegressionIsMismatch(t *testing.T) {
	r := check(t, "candidate-regression", workspace(t))
	if r.exit != 1 || r.report.Outcome != model.OutcomeMismatch {
		t.Fatalf("exit %d outcome %s, want 1 mismatch\n%s", r.exit, r.report.Outcome, r.stderr)
	}
	assertARMRun(t, r, sysroot)
	want := [][]model.CheckStatus{{model.CheckMatched, model.CheckMatched}, {model.CheckMatched, model.CheckMismatch}}
	if got := statuses(r.report); !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	failed := r.report.Variants[1].Tests[1]
	if *failed.Observation.ExitCode != 1 || !strings.Contains(r.log(t, failed.Observation.Stderr), "large-offset assertion failed: strtol") {
		t.Fatalf("candidate mismatch is not the named large-offset assertion: %+v", failed)
	}
}

// Preflight failures exit 2 before any command; a failing candidate exits 1.
func TestMissingEmulatorOrSysrootIsInfrastructure(t *testing.T) {
	for _, tt := range []struct {
		manifest, kind, message string
	}{
		{"missing-qemu", "missing_tool", "QEMU executable is unavailable"},
		{"missing-sysroot", "invalid_input", "QEMU sysroot is unavailable"},
	} {
		t.Run(tt.manifest, func(t *testing.T) {
			root := workspace(t)
			r := check(t, tt.manifest, root)
			if r.exit != 2 || r.report.Outcome != model.OutcomeError || r.report.Failure == nil || string(r.report.Failure.Kind) != tt.kind || !strings.HasPrefix(r.report.Failure.Message, tt.message) {
				t.Fatalf("exit %d outcome %s failure %+v, want 2 %s %q", r.exit, r.report.Outcome, r.report.Failure, tt.kind, tt.message)
			}
			if len(r.report.Variants) != 0 || len(r.report.Declared) != 0 || r.stderr != "targetcheck: error: "+tt.kind+": "+r.report.Failure.Message+"\n" {
				t.Fatalf("preflight failure report or diagnostic is wrong: %+v %q", r.report, r.stderr)
			}
			for _, dir := range []string{"baseline", "candidate"} {
				if _, err := os.Stat(filepath.Join(root, dir, "component")); !os.IsNotExist(err) {
					t.Fatalf("%s build ran despite the preflight failure", dir)
				}
			}
		})
	}
}

// Artifacts are inspected after their build; an ARM dynamic loader missing
// from the sysroot or a native artifact is an infrastructure failure.
func TestInvalidARMArtifactsStopBeforeTests(t *testing.T) {
	for _, tt := range []struct{ manifest, message string }{
		{"missing-loader", "ARM dynamic loader is missing from sysroot"},
		{"native-artifact", "ELF is not ARM32 EABI5 hard-float"},
	} {
		t.Run(tt.manifest, func(t *testing.T) {
			r := check(t, tt.manifest, workspace(t))
			f := r.report.Failure
			if r.exit != 2 || r.report.Outcome != model.OutcomeError || f == nil || f.Kind != model.FailureArtifact || !strings.Contains(f.Message, tt.message) {
				t.Fatalf("exit %d outcome %s failure %+v, want 2 invalid_artifact %q", r.exit, r.report.Outcome, f, tt.message)
			}
			baseline := r.report.Variants[0]
			if baseline.Build[0].Status != model.CheckMatched {
				t.Fatalf("baseline build should have run: %+v", baseline.Build[0])
			}
			assertQEMUVersion(t, r.report)
			for _, v := range r.report.Variants {
				for _, test := range v.Tests {
					if test.Status != model.CheckNotRun || test.Observation != nil {
						t.Fatalf("%s test %s ran after an invalid artifact", v.Name, test.ID)
					}
				}
			}
		})
	}
}

func TestStaticARMArtifactWithEmptySysroot(t *testing.T) {
	root := workspace(t)
	r := check(t, "static-empty-sysroot", root)
	if r.exit != 0 || r.report.Outcome != model.OutcomeMatched {
		t.Fatalf("exit %d outcome %s failure %+v", r.exit, r.report.Outcome, r.report.Failure)
	}
	assertARMRun(t, r, filepath.Join(root, "empty-sysroot"))
}
