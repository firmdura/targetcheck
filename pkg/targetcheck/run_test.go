//go:build linux

// SPDX-License-Identifier: Apache-2.0

package targetcheck_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	tc "github.com/firmdura/targetcheck/pkg/targetcheck"
)

var cliBinary string
var repoRoot string

func TestMain(m *testing.M) {
	var err error
	repoRoot, err = filepath.Abs("../..")
	if err != nil {
		panic(err)
	}
	tmp, err := os.MkdirTemp("", "targetcheck-cli-tests-")
	if err != nil {
		panic(err)
	}
	cliBinary = filepath.Join(tmp, "targetcheck")
	cmd := exec.Command("go", "build", "-o", cliBinary, "./cmd/targetcheck")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build integration CLI: %s: %v\n", out, err)
		os.RemoveAll(tmp)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func workspace(t *testing.T, baseline, candidate string) (string, tc.Spec) {
	t.Helper()
	root := t.TempDir()
	source, err := os.ReadFile(filepath.Join(repoRoot, "testdata/integration/helper.c"))
	if err != nil {
		t.Fatal(err)
	}
	variants := []tc.Variant{}
	for i, mode := range []string{baseline, candidate} {
		name := []string{"baseline", "candidate"}[i]
		dir := filepath.Join(root, name)
		must(t, os.Mkdir(dir, 0700))
		must(t, os.WriteFile(filepath.Join(dir, "helper.c"), source, 0600))
		must(t, os.WriteFile(filepath.Join(dir, "mode.txt"), []byte(mode), 0600))
		variants = append(variants, tc.Variant{CWD: name, Build: []tc.Command{{Argv: []string{"cc", "-O0", "-o", "runner", "helper.c"}, TimeoutSeconds: 20}}, Inputs: []string{"helper.c", "mode.txt"}, Artifacts: []string{"runner"}})
	}
	spec := tc.Spec{SchemaVersion: 1, Target: tc.Target{Kind: tc.TargetNative}, Variants: tc.Variants{Baseline: variants[0], Candidate: variants[1]}, TestInputs: []string{}, Tests: []tc.Test{{ID: "same regression", Argv: []string{"runner"}, TimeoutSeconds: 2, BaselineExpect: tc.Expectation{ExitCode: 0}, CandidateExpect: tc.Expectation{ExitCode: 0}}}}
	if baseline == "negative" {
		spec.Tests[0].BaselineExpect = tc.Expectation{ExitCode: 7, StdoutContains: "BASELINE_DEFECT"}
	}
	return root, spec
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func assertRender(t *testing.T, r tc.Report) {
	t.Helper()
	var j, m bytes.Buffer
	must(t, tc.WriteJSON(&j, r))
	must(t, tc.WriteMarkdown(&m, r))
	var decoded tc.Report
	must(t, json.Unmarshal(j.Bytes(), &decoded))
	if decoded.Outcome != r.Outcome || !strings.Contains(m.String(), string(r.Outcome)) {
		t.Fatal("JSON/Markdown disagree with report")
	}
	for i, v := range r.Variants {
		for k, c := range v.Tests {
			if decoded.Variants[i].Tests[k].Status != c.Status || !strings.Contains(html.UnescapeString(m.String()), string(c.Status)) {
				t.Fatal("lost test status")
			}
		}
	}
}

func TestRunNativeOutcomes(t *testing.T) {
	for _, c := range []struct {
		name, baseline, candidate string
		expected                  tc.Outcome
		failure                   tc.FailureKind
		message                   string
	}{
		{"expected negative", "negative", "pass", tc.OutcomeMatched, "", ""},
		{"unexpected baseline success", "pass", "pass", tc.OutcomeMismatch, "", ""},
		{"candidate failure", "negative", "fail", tc.OutcomeMismatch, "", ""},
		{"timeout", "sleep", "pass", tc.OutcomeError, tc.FailureTimeout, `baseline test "same regression": command exceeded its configured timeout`},
		{"output cap", "flood", "pass", tc.OutcomeError, tc.FailureOutputLimit, `baseline test "same regression": command exceeded the stdout or stderr capture limit`},
		{"signal", "signal", "pass", tc.OutcomeError, tc.FailureSignal, `baseline test "same regression": command terminated by signal terminated`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, spec := workspace(t, c.baseline, c.candidate)
			if c.name == "unexpected baseline success" {
				spec.Tests[0].BaselineExpect = tc.Expectation{ExitCode: 7, StdoutContains: "BASELINE_DEFECT"}
			}
			r, err := tc.Run(context.Background(), spec, tc.Options{Root: root})
			if r.Outcome != c.expected {
				t.Fatalf("outcome %s: %v %+v", r.Outcome, err, r.Failure)
			}
			if c.failure != "" {
				var re *tc.RunError
				if !errors.As(err, &re) || r.Failure.Kind != c.failure || r.Failure.Message != c.message || re.Failure != *r.Failure {
					t.Fatalf("failure: %v %+v, want %s %q", err, r.Failure, c.failure, c.message)
				}
				if r.Variants[1].Tests[0].Observation != nil {
					t.Fatal("candidate ran after infrastructure failure")
				}
				if len(r.Variants[0].ArtifactsAfter) == 0 {
					t.Fatal("lost final identity")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if c.expected == tc.OutcomeMismatch && r.Variants[1].Tests[0].Observation == nil {
				t.Fatal("behavior mismatch prevented candidate execution")
			}
			if len(r.Declared) != 2 || len(r.Inputs) != 4 || r.Manifest == nil {
				t.Fatal("lost declaration/input identity")
			}
			assertRender(t, r)
		})
	}
}

func TestFailedBuildStopsAndPreservesCoverage(t *testing.T) {
	root, spec := workspace(t, "pass", "pass")
	spec.Variants.Baseline.Build = []tc.Command{{Argv: []string{"/bin/sh", "-c", "echo BUILD_FAILED >&2; exit 23"}, TimeoutSeconds: 2}}
	r, err := tc.Run(context.Background(), spec, tc.Options{Root: root})
	var re *tc.RunError
	if !errors.As(err, &re) || re.Kind != tc.ErrorInfrastructure || r.Failure.Kind != tc.FailureBuild || r.Failure.Message != `baseline build "build-0": exit 23; expected 0` || r.Variants[0].Build[0].Observation == nil || *r.Variants[0].Build[0].Observation.ExitCode != 23 {
		t.Fatalf("build failure lost: %v %+v", err, r.Failure)
	}
	for _, v := range r.Variants {
		if v.Tests[0].Status != tc.CheckNotRun {
			t.Fatal("unexecuted test labelled completed")
		}
	}
	assertRender(t, r)
}

func TestRunArtifactMutation(t *testing.T) {
	root, spec := workspace(t, "mutate", "pass")
	spec.Variants.Baseline.Build = append(spec.Variants.Baseline.Build, tc.Command{Argv: []string{"/bin/sh", "-c", "cp runner replacement; printf x >> replacement"}, TimeoutSeconds: 2})
	r, err := tc.Run(context.Background(), spec, tc.Options{Root: root})
	if err == nil || r.Failure.Kind != tc.FailureIdentity || r.Failure.Message != `baseline test "same regression": artifact identity changed during testing: baseline/runner` || r.Variants[0].Tests[0].Status != tc.CheckError || r.Variants[1].Build[0].Observation != nil {
		t.Fatalf("mutation: %v %+v", err, r.Failure)
	}
	assertRender(t, r)
}

func TestRunConcurrentNoGlobalMutation(t *testing.T) {
	cwd, err := os.Getwd()
	must(t, err)
	env := os.Environ()
	root1, spec1 := workspace(t, "negative", "pass")
	root2, spec2 := workspace(t, "negative", "pass")
	spec1.Variants.Baseline.Build[0].Env = map[string]string{"TARGETCHECK_OVERRIDE": "private-value-one"}
	spec2.Variants.Baseline.Build[0].Env = map[string]string{"TARGETCHECK_OVERRIDE": "private-value-two"}
	var wg sync.WaitGroup
	for i, item := range []struct {
		root string
		spec tc.Spec
	}{{root1, spec1}, {root2, spec2}} {
		wg.Add(1)
		go func(i int, item struct {
			root string
			spec tc.Spec
		}) {
			defer wg.Done()
			r, err := tc.Run(context.Background(), item.spec, tc.Options{Root: item.root})
			if err != nil || r.Outcome != tc.OutcomeMatched {
				t.Errorf("concurrent %d: %v %+v", i, err, r.Failure)
			}
			var b bytes.Buffer
			if err := tc.WriteJSON(&b, r); err != nil {
				t.Error(err)
			}
			if strings.Contains(b.String(), "private-value") {
				t.Error("serialized environment value")
			}
		}(i, item)
	}
	wg.Wait()
	after, err := os.Getwd()
	must(t, err)
	if after != cwd || !reflect.DeepEqual(env, os.Environ()) {
		t.Fatal("Run changed cwd/environment")
	}
}

func TestRunCancellation(t *testing.T) {
	root, spec := workspace(t, "sleep", "pass")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var r tc.Report
	var err error
	go func() { r, err = tc.Run(ctx, spec, tc.Options{Root: root}); close(done) }()
	waitFile(t, filepath.Join(root, "baseline/started"), done)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation not bounded")
	}
	if !errors.Is(err, context.Canceled) || err.Error() != "cancelled: run cancelled by caller" || r.Outcome != tc.OutcomeCancelled || r.Variants[1].Build[0].Observation != nil {
		t.Fatalf("cancel: %v %+v", err, r.Failure)
	}
	assertRender(t, r)
}
func waitFile(t *testing.T, path string, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-done:
			t.Fatal("command exited before marker")
		case <-timer.C:
			t.Fatal("command did not create marker")
		case <-tick.C:
		}
	}
}

func runCLI(t *testing.T, ctx context.Context, manifest, root, out string) (tc.Report, int, string, string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, cliBinary, "check", "--manifest", manifest, "--root", root, "--output", out)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	var r tc.Report
	if data, e := os.ReadFile(filepath.Join(out, "report.json")); e == nil {
		must(t, json.Unmarshal(data, &r))
		md, e := os.ReadFile(filepath.Join(out, "report.md"))
		must(t, e)
		if !strings.Contains(string(md), string(r.Outcome)) {
			t.Fatal("CLI JSON/MD outcome disagreement")
		}
	}
	return r, code, stdout.String(), stderr.String()
}
func manifestFile(t *testing.T, root string, spec tc.Spec) string {
	t.Helper()
	data, err := json.Marshal(spec)
	must(t, err)
	name := filepath.Join(root, "manifest.json")
	must(t, os.WriteFile(name, data, 0600))
	return name
}

func TestCLINativeMatrix(t *testing.T) {
	for _, c := range []struct {
		name, baseline, candidate string
		code                      int
		kind                      tc.FailureKind
		message                   string
	}{
		{"matched", "negative", "pass", 0, "", ""},
		{"unexpected baseline", "pass", "pass", 1, "", ""},
		{"candidate failure", "negative", "fail", 1, "", ""},
		{"timeout", "sleep", "pass", 2, tc.FailureTimeout, `baseline test "same regression": command exceeded its configured timeout`},
		{"output cap", "flood", "pass", 2, tc.FailureOutputLimit, `baseline test "same regression": command exceeded the stdout or stderr capture limit`},
		{"missing tool", "pass", "pass", 2, tc.FailureMissingTool, "candidate build-0 executable is unavailable"},
		{"failed build", "pass", "pass", 2, tc.FailureBuild, `baseline build "build-0": exit 23; expected 0`},
		{"mutation", "mutate", "pass", 2, tc.FailureIdentity, `baseline test "same regression": artifact identity changed during testing: baseline/runner`},
		{"malformed", "pass", "pass", 2, tc.FailureInvalidInput, `load manifest: invalid JSON manifest: duplicate key "schema_version"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, spec := workspace(t, c.baseline, c.candidate)
			if c.name == "unexpected baseline" {
				spec.Tests[0].BaselineExpect = tc.Expectation{ExitCode: 7, StdoutContains: "BASELINE_DEFECT"}
			}
			if c.name == "missing tool" {
				spec.Variants.Candidate.Build[0].Argv[0] = "/missing-targetcheck-tool"
			}
			if c.name == "failed build" {
				spec.Variants.Baseline.Build[0].Argv = []string{"/bin/sh", "-c", "echo BUILD_FAILED >&2; exit 23"}
			}
			if c.name == "mutation" {
				spec.Variants.Baseline.Build = append(spec.Variants.Baseline.Build, tc.Command{Argv: []string{"/bin/sh", "-c", "cp runner replacement; printf x >> replacement"}, TimeoutSeconds: 2})
			}
			manifest := manifestFile(t, root, spec)
			if c.name == "malformed" {
				must(t, os.WriteFile(manifest, []byte(`{"schema_version":1,"schema_version":1}`), 0600))
			}
			out := filepath.Join(root, "out")
			if c.name == "malformed" {
				out = filepath.Join(t.TempDir(), "out")
			}
			r, code, stdout, stderr := runCLI(t, context.Background(), manifest, root, out)
			if code != c.code {
				t.Fatalf("exit %d expected %d stdout=%s stderr=%s", code, c.code, stdout, stderr)
			}
			expectedOutcome := map[int]tc.Outcome{0: tc.OutcomeMatched, 1: tc.OutcomeMismatch, 2: tc.OutcomeError}[c.code]
			if r.Outcome != expectedOutcome {
				t.Fatalf("outcome %s expected %s", r.Outcome, expectedOutcome)
			}
			if r.SchemaVersion != 1 {
				t.Fatalf("missing failure/report evidence: %s", stderr)
			}
			if c.kind != "" && (r.Failure == nil || r.Failure.Kind != c.kind || r.Failure.Message != c.message) {
				t.Fatalf("cause %+v, want %s %q", r.Failure, c.kind, c.message)
			}
			// One diagnostic line states the outcome, the specific kind and the
			// report's message; matched and mismatched runs print none.
			wantStderr := ""
			if c.code == 2 {
				wantStderr = "targetcheck: error: " + string(c.kind) + ": " + c.message + "\n"
			}
			if stderr != wantStderr {
				t.Fatalf("stderr %q, want %q", stderr, wantStderr)
			}
			if !strings.HasPrefix(stdout, "targetcheck: "+string(expectedOutcome)+"\nJSON: ") {
				t.Fatalf("stdout summary %q", stdout)
			}
			if strings.Contains(stdout, "BASELINE_DEFECT") || strings.Contains(stdout, "CANDIDATE_FAIL") {
				t.Fatal("raw logs leaked to stdout")
			}
			if c.name == "missing tool" || c.name == "malformed" {
				if _, err := os.Stat(filepath.Join(root, "baseline/runner")); !os.IsNotExist(err) {
					t.Fatal("invalid preflight ran build")
				}
			}
			for _, v := range r.Variants {
				for _, checks := range [][]tc.Check{v.Build, v.Tests} {
					for _, check := range checks {
						if check.Observation == nil {
							continue
						}
						for _, capture := range []tc.Capture{check.Observation.Stdout, check.Observation.Stderr} {
							if capture.Log == "" {
								t.Fatal("missing log reference")
							}
							info, err := os.Stat(filepath.Join(out, capture.Log))
							must(t, err)
							if info.Mode().Perm() != 0600 {
								t.Fatal("log permissions")
							}
						}
					}
				}
			}
		})
	}
}

// The library's typed error and the CLI's report and diagnostic agree on the
// category, the specific failure kind and the message.
func TestLibraryAndCLIAgreeOnFailures(t *testing.T) {
	for _, c := range []struct {
		name, baseline string
		change         func(*tc.Spec)
		category       tc.ErrorKind
		kind           tc.FailureKind
	}{
		{"missing tool", "pass", func(s *tc.Spec) { s.Variants.Candidate.Build[0].Argv[0] = "/missing-targetcheck-tool" }, tc.ErrorInvalidInput, tc.FailureMissingTool},
		{"failed build", "pass", func(s *tc.Spec) { s.Variants.Baseline.Build[0].Argv = []string{"/bin/sh", "-c", "exit 23"} }, tc.ErrorInfrastructure, tc.FailureBuild},
		{"timeout", "sleep", func(*tc.Spec) {}, tc.ErrorInfrastructure, tc.FailureTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, spec := workspace(t, c.baseline, "pass")
			c.change(&spec)
			_, err := tc.Run(context.Background(), spec, tc.Options{Root: root})
			var re *tc.RunError
			if !errors.As(err, &re) || re.Kind != c.category || re.Failure.Kind != c.kind {
				t.Fatalf("library error %v", err)
			}
			if want := string(c.category) + ": " + string(c.kind) + ": " + re.Failure.Message; err.Error() != want {
				t.Fatalf("Error() %q, want %q", err.Error(), want)
			}
			cliRoot, cliSpec := workspace(t, c.baseline, "pass")
			c.change(&cliSpec)
			r, code, _, stderr := runCLI(t, context.Background(), manifestFile(t, cliRoot, cliSpec), cliRoot, filepath.Join(cliRoot, "out"))
			if code != 2 || r.Failure == nil || *r.Failure != re.Failure || stderr != "targetcheck: error: "+string(c.kind)+": "+re.Failure.Message+"\n" {
				t.Fatalf("CLI exit %d failure %+v stderr %q, library %+v", code, r.Failure, stderr, re.Failure)
			}
		})
	}
}

// defaultDispositions makes the CLI processes that a test starts begin with
// SIGINT and SIGHUP at their default disposition, even when this test binary
// itself was started with them ignored, as under nohup or in a background job
// of a non-interactive shell: a forked child keeps an inherited ignore, but
// resets every signal that its parent handles to the default before exec.
func defaultDispositions(t *testing.T) {
	t.Helper()
	handled := make(chan os.Signal, 1)
	signal.Notify(handled, os.Interrupt, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(handled) })
}

// SIGINT, SIGTERM and SIGHUP all cancel the run: the CLI stops the running
// command's process group, finalizes both reports and exits 130.
func TestCLISignalsCancel(t *testing.T) {
	defaultDispositions(t)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			root, spec := workspace(t, "sleep", "pass")
			manifest := manifestFile(t, root, spec)
			out := filepath.Join(root, "out")
			cmd := exec.Command(cliBinary, "check", "--manifest", manifest, "--root", root, "--output", out)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			must(t, cmd.Start())
			done := make(chan struct{})
			var err error
			go func() { err = cmd.Wait(); close(done) }()
			helpers := runningHelpers(t, root, done)
			must(t, cmd.Process.Signal(sig))
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				cmd.Process.Kill()
				t.Fatalf("%s did not clean up", sig)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 130 {
				t.Fatalf("exit: %v %s", err, stderr.String())
			}
			if stderr.String() != "targetcheck: cancelled: run cancelled by caller\n" || !strings.HasPrefix(stdout.String(), "targetcheck: cancelled\nJSON: ") {
				t.Fatalf("console stdout %q stderr %q", stdout.String(), stderr.String())
			}
			assertCancelledReports(t, out)
			assertStopped(t, helpers)
		})
	}
}

// Closing a terminal hangs up its session: the kernel sends SIGHUP to the
// session leader only, not to the separate process groups of the commands the
// CLI started. The CLI must stop them itself, finalize the reports and exit 130
// even though its terminal output now fails.
func TestCLITerminalHangupStopsCommandGroups(t *testing.T) {
	defaultDispositions(t)
	root, spec := workspace(t, "sleep", "pass")
	manifest := manifestFile(t, root, spec)
	out := filepath.Join(root, "out")
	master, terminal := openPTY(t)
	cmd := exec.Command(cliBinary, "check", "--manifest", manifest, "--root", root, "--output", out)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = terminal, terminal, terminal
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	must(t, cmd.Start())
	terminal.Close()
	done := make(chan struct{})
	var err error
	go func() { err = cmd.Wait(); close(done) }()
	helpers := runningHelpers(t, root, done)
	must(t, master.Close()) // the terminal closes
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		t.Fatal("terminal hangup did not clean up")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 130 {
		t.Fatalf("exit after terminal hangup: %v", err)
	}
	assertCancelledReports(t, out)
	assertStopped(t, helpers)
}

// A signal that was ignored when the CLI started stays ignored: nohup ignores
// SIGHUP, and non-interactive shells start background jobs with SIGINT ignored.
// The run then continues to its normal result. The Go runtime keeps such an
// inherited ignore only for SIGHUP and SIGINT, so SIGTERM still cancels.
func TestCLIKeepsSignalsIgnoredAtStart(t *testing.T) {
	nohup, err := exec.LookPath("nohup")
	if err != nil {
		t.Fatalf("nohup is required: %v", err)
	}
	ignoring := func(trap string) []string {
		return []string{"/bin/sh", "-c", `trap "" ` + trap + `; exec "$0" "$@"`}
	}
	for _, c := range []struct {
		name   string
		prefix []string
		sig    os.Signal
		code   int
	}{
		{"SIGHUP under nohup", []string{nohup}, syscall.SIGHUP, 0},
		{"SIGHUP ignored before exec", ignoring("HUP"), syscall.SIGHUP, 0},
		{"SIGINT ignored before exec", ignoring("INT"), os.Interrupt, 0},
		{"SIGTERM ignored before exec still cancels", ignoring("TERM"), syscall.SIGTERM, 130},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, spec := workspace(t, "negative", "pass")
			// The signal arrives while the first build waits; the run must then
			// finish both variants exactly as without the signal.
			window := tc.Command{Argv: []string{"/bin/sh", "-c", "touch signal-window; sleep 1"}, TimeoutSeconds: 10}
			spec.Variants.Baseline.Build = append([]tc.Command{window}, spec.Variants.Baseline.Build...)
			manifest := manifestFile(t, root, spec)
			out := filepath.Join(root, "out")
			argv := append(append([]string{}, c.prefix...), cliBinary, "check", "--manifest", manifest, "--root", root, "--output", out)
			cmd := exec.Command(argv[0], argv[1:]...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			must(t, cmd.Start())
			done := make(chan struct{})
			var err error
			go func() { err = cmd.Wait(); close(done) }()
			waitFile(t, filepath.Join(root, "baseline/signal-window"), done)
			must(t, cmd.Process.Signal(c.sig))
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				cmd.Process.Kill()
				t.Fatal("run did not finish")
			}
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.code {
				t.Fatalf("exit %d, want %d: stdout %q stderr %q", code, c.code, stdout.String(), stderr.String())
			}
			if c.code == 130 {
				assertCancelledReports(t, out)
				return
			}
			data, err := os.ReadFile(filepath.Join(out, "report.json"))
			must(t, err)
			var r tc.Report
			must(t, json.Unmarshal(data, &r))
			if r.Outcome != tc.OutcomeMatched || r.Failure != nil || r.Variants[1].Tests[0].Status != tc.CheckMatched || stderr.String() != "" || !strings.HasPrefix(stdout.String(), "targetcheck: matched\nJSON: ") {
				t.Fatalf("ignored %s changed the run: outcome %s %+v stdout %q stderr %q", c.sig, r.Outcome, r.Failure, stdout.String(), stderr.String())
			}
		})
	}
}

func openPTY(t *testing.T) (master, terminal *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	must(t, err)
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Fatalf("unlock pseudo-terminal: %v", e)
	}
	var number uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); e != 0 {
		t.Fatalf("name pseudo-terminal: %v", e)
	}
	terminal, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	must(t, err)
	t.Cleanup(func() { master.Close(); terminal.Close() })
	return master, terminal
}

// runningHelpers waits until the baseline test helper runs and returns the
// processes executing it.
func runningHelpers(t *testing.T, root string, done <-chan struct{}) []int {
	t.Helper()
	waitFile(t, filepath.Join(root, "baseline/started"), done)
	runner, err := filepath.EvalSymlinks(filepath.Join(root, "baseline/runner"))
	must(t, err)
	pids := processesExecuting(runner)
	if len(pids) == 0 {
		t.Fatal("no process executes the started test helper")
	}
	t.Cleanup(func() {
		for _, pid := range processesExecuting(runner) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pids
}

func processesExecuting(path string) []int {
	var pids []int
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil && exe == path {
			pids = append(pids, pid)
		}
	}
	return pids
}

// assertStopped requires that no started command outlives the CLI, as a command
// group orphaned by an unhandled signal would.
func assertStopped(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for _, pid := range pids {
		for {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("test helper %d still runs after the CLI exited", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func assertCancelledReports(t *testing.T, out string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, "report.json"))
	must(t, err)
	var r tc.Report
	must(t, json.Unmarshal(data, &r))
	if r.Outcome != tc.OutcomeCancelled || r.Failure == nil || r.Failure.Kind != tc.FailureCancelled || r.Variants[1].Build[0].Status != tc.CheckNotRun {
		t.Fatalf("cancel report lost partial coverage: %+v", r.Failure)
	}
	md, err := os.ReadFile(filepath.Join(out, "report.md"))
	must(t, err)
	if !strings.Contains(string(md), "cancelled") {
		t.Fatal("markdown cancellation lost")
	}
	if _, err := os.Stat(filepath.Join(out, "INCOMPLETE")); !os.IsNotExist(err) {
		t.Fatalf("cancelled reports were not finalized: %v", err)
	}
}

func TestCLIExistingAndUnsafeOutput(t *testing.T) {
	root, spec := workspace(t, "pass", "pass")
	manifest := manifestFile(t, root, spec)
	for _, name := range []string{"existing", "symlink", "baseline/nested"} {
		out := filepath.Join(root, name)
		if name == "existing" {
			must(t, os.Mkdir(out, 0700))
			must(t, os.WriteFile(filepath.Join(out, "preserve"), []byte("do not touch"), 0600))
		}
		if name == "symlink" {
			must(t, os.Symlink(filepath.Join(root, "baseline"), out))
		}
		_, code, _, _ := runCLI(t, context.Background(), manifest, root, out)
		if code != 2 {
			t.Fatalf("unsafe output exit %d", code)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "existing/preserve")); err != nil || string(data) != "do not touch" {
		t.Fatal("changed existing data")
	}
	if _, err := os.Stat(filepath.Join(root, "baseline/runner")); !os.IsNotExist(err) {
		t.Fatal("output rejection launched build")
	}
}

func TestCLIPermissionFailure(t *testing.T) {
	root, spec := workspace(t, "pass", "pass")
	manifest := manifestFile(t, root, spec)
	parent := filepath.Join(root, "readonly")
	must(t, os.Mkdir(parent, 0500))
	defer os.Chmod(parent, 0700)
	cmd := exec.Command(cliBinary, "check", "--manifest", manifest, "--root", root, "--output", filepath.Join(parent, "out"))
	// Root bypasses DAC. Run as nobody when necessary to exercise an actual denial.
	if os.Geteuid() == 0 {
		for path := root; path != filepath.Dir(os.TempDir()) && path != os.TempDir(); path = filepath.Dir(path) {
			must(t, os.Chmod(path, 0755))
		}
		for _, name := range []string{"baseline", "candidate"} {
			must(t, os.Chmod(filepath.Join(root, name), 0755))
			for _, file := range []string{"mode.txt", "helper.c"} {
				must(t, os.Chmod(filepath.Join(root, name, file), 0644))
			}
		}
		must(t, os.Chmod(manifest, 0644))
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		must(t, os.Chmod(filepath.Dir(cliBinary), 0755))
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("permission denial %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "baseline/runner")); !os.IsNotExist(err) {
		t.Fatal("permission failure ran build")
	}
}

func TestLibraryOutputOptionDoesNotWrite(t *testing.T) {
	root, spec := workspace(t, "negative", "pass")
	out := filepath.Join(root, "reports")
	r, err := tc.Run(context.Background(), spec, tc.Options{Root: root, OutputDir: out})
	if err != nil || r.Outcome != tc.OutcomeMatched {
		t.Fatalf("run: %v %+v", err, r.Failure)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("library wrote output")
	}
}

func TestLibraryLiteralArgv(t *testing.T) {
	root, spec := workspace(t, "pass", "pass")
	spec.Tests[0].Argv = []string{"runner", "args", "space in argument", ";$(touch SHOULD_NOT_EXIST)", ""}
	spec.Tests[0].BaselineExpect.StdoutContains = "space in argument|;$(touch SHOULD_NOT_EXIST)|\n"
	spec.Tests[0].CandidateExpect = spec.Tests[0].BaselineExpect
	r, err := tc.Run(context.Background(), spec, tc.Options{Root: root})
	if err != nil || r.Outcome != tc.OutcomeMatched {
		t.Fatalf("literal argv: %v %+v", err, r.Failure)
	}
	for _, name := range []string{"baseline", "candidate"} {
		if _, err := os.Stat(filepath.Join(root, name, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
			t.Fatal("argument shell expanded")
		}
	}
}

func TestCLIReportWriteFailure(t *testing.T) {
	root, spec := workspace(t, "negative", "pass")
	out := filepath.Join(root, "out")
	// Trusted build commands can also disrupt the output destination. A real
	// report-file collision must outrank otherwise matched test expectations.
	spec.Variants.Baseline.Build = append(spec.Variants.Baseline.Build, tc.Command{Argv: []string{"mkdir", filepath.Join(out, "report.md")}, TimeoutSeconds: 2})
	manifest := manifestFile(t, root, spec)
	cmd := exec.Command(cliBinary, "check", "--manifest", manifest, "--root", root, "--output", out)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("write failure: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(out, "report.json"))
	must(t, err)
	var r tc.Report
	must(t, json.Unmarshal(data, &r))
	if r.Outcome != tc.OutcomeError || r.Failure == nil || r.Failure.Kind != tc.FailureIO {
		t.Fatalf("write failure report: %+v", r.Failure)
	}
	for _, v := range r.Variants {
		if v.Tests[0].Status != tc.CheckMatched {
			t.Fatal("lost completed behavioral observation")
		}
	}
	if _, err := os.Stat(filepath.Join(out, "INCOMPLETE")); err != nil {
		t.Fatal("missing incomplete output marker")
	}
	if !strings.Contains(stderr.String(), "incomplete output") {
		t.Fatal("missing actionable incomplete diagnostic")
	}
}
