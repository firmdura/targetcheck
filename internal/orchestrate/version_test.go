//go:build linux

// SPDX-License-Identifier: Apache-2.0

package orchestrate

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/firmdura/targetcheck/internal/evaluate"
	"github.com/firmdura/targetcheck/internal/execute"
	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/plan"
)

// A stand-in emulator is a shell script: QEMU --version runs the case's version
// script, and any other invocation is a test run that passes. The ARM headers
// are structural fixtures, never executed; this is not ARM/QEMU evidence.
type qemuStandIn struct {
	spec    model.Spec
	options model.Options
	qemu    string
	order   string // one line per reservation, version query, build and test
	pidFile string // PID of the version query process
}

func armHeader() []byte {
	data := make([]byte, 52)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 40)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint32(data[24:], 0x10000)
	binary.LittleEndian.PutUint32(data[36:], 0x05000400)
	binary.LittleEndian.PutUint16(data[40:], 52)
	binary.LittleEndian.PutUint16(data[42:], 32)
	binary.LittleEndian.PutUint16(data[46:], 40)
	return data
}

func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newQEMUStandIn(t *testing.T, versionScript string) qemuStandIn {
	t.Helper()
	root, aux := realDir(t), realDir(t)
	s := qemuStandIn{qemu: filepath.Join(aux, "qemu-arm"), order: filepath.Join(aux, "order"), pidFile: filepath.Join(aux, "version.pid")}
	sysroot := filepath.Join(aux, "sysroot")
	if err := os.Mkdir(sysroot, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then\n\techo version >> " + s.order + "\n\techo $$ > " + s.pidFile + "\n" + versionScript + "\nfi\necho \"test $4\" >> " + s.order + "\nexit 0\n"
	if err := os.WriteFile(s.qemu, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	variant := func(name string) model.Variant {
		for file, data := range map[string][]byte{"input": []byte("declared"), "component": armHeader()} {
			if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name, file), data, 0700); err != nil {
				t.Fatal(err)
			}
		}
		return model.Variant{CWD: name, Build: []model.Command{{Argv: []string{"/bin/sh", "-c", "echo build >> " + s.order}, TimeoutSeconds: 5}}, Inputs: []string{"input"}, Artifacts: []string{"component"}}
	}
	s.spec = model.Spec{SchemaVersion: 1, Target: model.Target{Kind: model.TargetQEMUARM, Executable: s.qemu, Sysroot: sysroot}, Variants: model.Variants{Baseline: variant("baseline"), Candidate: variant("candidate")}, TestInputs: []string{},
		Tests: []model.Test{{ID: "case", Argv: []string{"component", "case"}, TimeoutSeconds: 5, BaselineExpect: model.Expectation{ExitCode: 0}, CandidateExpect: model.Expectation{ExitCode: 0}}}}
	s.options = model.Options{Root: root}
	return s
}

func (s qemuStandIn) lines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.order)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func qemuTool(t *testing.T, r model.Report) model.ToolIdentity {
	t.Helper()
	var found []model.ToolIdentity
	for _, tool := range r.Tools {
		if tool.Role == "qemu" {
			found = append(found, tool)
		}
	}
	if len(found) != 1 || found[0].Path != r.Target.Executable || (found[0].Version == "") == (found[0].VersionError == "") {
		t.Fatalf("qemu tool observations %+v, want one with exactly one of version and version_error", found)
	}
	return found[0]
}

// recordingExecutor runs the real executor and records each request in order.
type recordingExecutor struct {
	mu       sync.Mutex
	requests []model.CommandRequest
}

func (e *recordingExecutor) Execute(ctx context.Context, request model.CommandRequest) model.ProcessObservation {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	return execute.Execute(ctx, request)
}

// After preflight and the reservation hook, and before the first build, the
// configured emulator runs once with --version through the normal executor: its
// own process group, the inherited environment without overrides, cwd Root and
// the fixed 10 s timeout. Its first non-empty stdout line becomes the version.
func TestQEMUVersionIsQueriedOnceAfterPreflight(t *testing.T) {
	t.Setenv("TARGETCHECK_INHERITED_PROBE", "inherited value")
	probe := filepath.Join(realDir(t), "probe")
	s := newQEMUStandIn(t, `	set -- $(cat /proc/$$/stat)
	printf 'cwd=%s env=%s pid=%s pgrp=%s\n' "$(pwd -P)" "$TARGETCHECK_INHERITED_PROBE" "$$" "$5" > `+probe+`
	printf '\n  qemu-arm version 9.9.9 (stand-in)  \r\nCopyright (c) stand-in\n'
	echo 'stderr is not the version' >&2
	exit 0`)
	executor := &recordingExecutor{}
	r, err := (Engine{Executor: executor}).Run(context.Background(), s.spec, s.options, func() error {
		return os.WriteFile(s.order, []byte("reserve\n"), 0600)
	})
	if err != nil || r.Outcome != model.OutcomeMatched {
		t.Fatalf("run: %v %+v", err, r.Failure)
	}
	if tool := qemuTool(t, r); tool.Version != "qemu-arm version 9.9.9 (stand-in)" {
		t.Fatalf("recorded version %+v", tool)
	}
	if got, want := s.lines(t), []string{"reserve", "version", "build", "test case", "build", "test case"}; !slices.Equal(got, want) {
		t.Fatalf("process order %q, want %q", got, want)
	}
	query := executor.requests[0]
	want := model.CommandRequest{Kind: model.CheckBuild, ID: "qemu-version", Target: r.Target, Argv: []string{s.qemu, "--version"}, ExecutablePath: s.qemu, AbsoluteCWD: s.options.Root, RelativeCWD: ".", TimeoutSeconds: 10}
	if query.Env != nil || query.Kind != want.Kind || query.ID != want.ID || query.Target != want.Target || !slices.Equal(query.Argv, want.Argv) || query.ExecutablePath != want.ExecutablePath || query.AbsoluteCWD != want.AbsoluteCWD || query.RelativeCWD != want.RelativeCWD || query.TimeoutSeconds != want.TimeoutSeconds {
		t.Fatalf("version query request %+v, want %+v", query, want)
	}
	for _, request := range executor.requests[1:] {
		if slices.Contains(request.Argv, "--version") {
			t.Fatalf("emulator queried more than once: %+v", request)
		}
	}
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) != 5 || fields[0] != "cwd="+s.options.Root || fields[1] != "env=inherited" || fields[2] != "value" || strings.TrimPrefix(fields[3], "pid=") != strings.TrimPrefix(fields[4], "pgrp=") {
		t.Fatalf("version query environment %q: want cwd Root, the inherited environment and its own process group", data)
	}
}

// A failed, silent or unusable version query records a precise version_error,
// and the run continues to its ordinary verdict.
func TestQEMUVersionFailuresNeverFailTheRun(t *testing.T) {
	for _, c := range []struct {
		name, script, versionError string
	}{
		{"nonzero exit", "\techo 'qemu-arm version 9.9.9'\n\texit 3", "--version query failed: exit 3; expected 0"},
		{"no output", "\tprintf '  \\n\\n'\n\texit 0", "--version query printed no non-empty stdout line"},
		{"stderr only", "\techo 'qemu-arm version 9.9.9' >&2\n\texit 0", "--version query printed no non-empty stdout line"},
		{"signal", "\tkill -KILL $$", "--version query failed: signal: command terminated by signal killed"},
		{"oversized line", "\tprintf '%0257d\\n' 0\n\texit 0", "--version query printed a first non-empty stdout line longer than 256 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newQEMUStandIn(t, c.script)
			r, err := (Engine{}).Run(context.Background(), s.spec, s.options, nil)
			if err != nil || r.Outcome != model.OutcomeMatched {
				t.Fatalf("version evidence failed the run: %v %+v", err, r.Failure)
			}
			if tool := qemuTool(t, r); tool.VersionError != c.versionError {
				t.Fatalf("version_error %q, want %q", tool.VersionError, c.versionError)
			}
			if got := s.lines(t); len(got) != 5 || got[0] != "version" {
				t.Fatalf("process order %q", got)
			}
		})
	}
}

func TestQEMUVersionQueryTimeout(t *testing.T) {
	s := newQEMUStandIn(t, "\texec sleep 30")
	p, err := plan.Prepare(s.spec, s.options)
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	version, versionError := queryQEMUVersion(context.Background(), execute.Executor{}, p, 1)
	if version != "" || versionError != "--version query failed: timeout: command exceeded its configured timeout (1 s)" {
		t.Fatalf("version %q error %q", version, versionError)
	}
	if elapsed := time.Since(begin); elapsed > 4*time.Second {
		t.Fatalf("timed-out query returned after %s", elapsed)
	}
	assertProcessGone(t, s.pidFile)
}

// Cancellation during the query cancels the run as anywhere else: every
// declared check stays not_run and the CLI exit is 130.
func TestQEMUVersionQueryCancellationCancelsTheRun(t *testing.T) {
	s := newQEMUStandIn(t, "\texec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if data, err := os.ReadFile(s.pidFile); err == nil && len(data) > 0 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	begin := time.Now()
	r, err := (Engine{}).Run(ctx, s.spec, s.options, nil)
	var re *model.RunError
	if !errors.As(err, &re) || re.Kind != model.ErrorCancelled || !errors.Is(err, context.Canceled) || r.Outcome != model.OutcomeCancelled || evaluate.ExitCode(r) != 130 {
		t.Fatalf("cancelled query: %v %+v", err, r.Failure)
	}
	if elapsed := time.Since(begin); elapsed > 4*time.Second {
		t.Fatalf("cancelled query returned after %s", elapsed)
	}
	if tool := qemuTool(t, r); tool.VersionError != "--version query failed: cancelled: command cancelled by caller" {
		t.Fatalf("qemu tool %+v", tool)
	}
	if len(r.Declared) != 2 || len(r.Variants) != 2 {
		t.Fatalf("declared coverage lost: %+v", r.Declared)
	}
	for _, v := range r.Variants {
		for _, check := range append(slices.Clone(v.Build), v.Tests...) {
			if check.Status != model.CheckNotRun || check.Observation != nil {
				t.Fatalf("%s %s %s is %s after cancellation during the version query", v.Name, check.Kind, check.ID, check.Status)
			}
		}
	}
	if got := s.lines(t); !slices.Equal(got, []string{"version"}) {
		t.Fatalf("commands ran after cancellation: %q", got)
	}
	assertProcessGone(t, s.pidFile)
}

// Failed preflight, or a failed reservation hook, starts no process at all: not
// even the version query.
func TestQEMUVersionIsNotQueriedWithoutSuccessfulPreflight(t *testing.T) {
	for name, change := range map[string]func(*qemuStandIn){
		"missing input":   func(s *qemuStandIn) { s.spec.Variants.Candidate.Inputs = []string{"missing"} },
		"missing sysroot": func(s *qemuStandIn) { s.spec.Target.Sysroot = filepath.Join(s.options.Root, "missing-sysroot") },
		"nested variants": func(s *qemuStandIn) { s.spec.Variants.Candidate.CWD = "baseline/candidate" },
	} {
		t.Run(name, func(t *testing.T) {
			s := newQEMUStandIn(t, "\techo 'qemu-arm version 9.9.9'")
			change(&s)
			executor := &recordingExecutor{}
			hook := 0
			r, err := (Engine{Executor: executor}).Run(context.Background(), s.spec, s.options, func() error { hook++; return nil })
			if err == nil || r.Outcome != model.OutcomeError || len(r.Tools) != 0 {
				t.Fatalf("preflight failure: %v %+v", err, r)
			}
			if len(executor.requests) != 0 || hook != 0 || s.lines(t) != nil {
				t.Fatalf("failed preflight started %d processes: %q", len(executor.requests), s.lines(t))
			}
		})
	}
	s := newQEMUStandIn(t, "\techo 'qemu-arm version 9.9.9'")
	executor := &recordingExecutor{}
	denied := errors.New("destination reservation denied")
	r, err := (Engine{Executor: executor}).Run(context.Background(), s.spec, s.options, func() error { return denied })
	if !errors.Is(err, denied) || len(executor.requests) != 0 || s.lines(t) != nil {
		t.Fatalf("failed reservation started processes: %v %d %q", err, len(executor.requests), s.lines(t))
	}
	if tool := qemuTool(t, r); tool.VersionError != "not queried: the run stopped before the --version query" {
		t.Fatalf("qemu tool after a stop before the query %+v", tool)
	}
}

func TestQEMUVersionLineSelection(t *testing.T) {
	normal := func(exit int, stdout string) model.ProcessObservation {
		return model.ProcessObservation{Started: true, ExitCode: &exit, Stdout: model.Capture{Data: []byte(stdout), Bytes: int64(len(stdout))}}
	}
	failed := func(kind model.FailureKind, message string) model.ProcessObservation {
		return model.ProcessObservation{Started: kind != model.FailureLaunch, Failure: &model.Failure{Kind: kind, Message: message}}
	}
	for _, c := range []struct {
		name                  string
		observation           model.ProcessObservation
		version, versionError string
	}{
		{"first line", normal(0, "qemu-arm version 7.2.22 (Debian 1:7.2+dfsg-7+deb12u18)\nCopyright (c) 2003-2022 Fabrice Bellard and the QEMU Project developers\n"), "qemu-arm version 7.2.22 (Debian 1:7.2+dfsg-7+deb12u18)", ""},
		{"blank lines and CRLF", normal(0, "\r\n \t\r\n  v1  \r\nv2\r\n"), "v1", ""},
		{"no trailing newline", normal(0, "v1"), "v1", ""},
		{"exactly 256 bytes", normal(0, strings.Repeat("v", 256)+"\n"), strings.Repeat("v", 256), ""},
		{"257 bytes", normal(0, strings.Repeat("v", 257)+"\n"), "", "--version query printed a first non-empty stdout line longer than 256 bytes"},
		{"invalid UTF-8", normal(0, "qemu \xff\n"), "", "--version query printed a first non-empty stdout line that is not valid UTF-8"},
		{"nothing", normal(0, ""), "", "--version query printed no non-empty stdout line"},
		{"nonzero exit", normal(1, "v1\n"), "", "--version query failed: exit 1; expected 0"},
		{"launch failure", failed(model.FailureLaunch, "cannot launch command (check executable, cwd and permissions): permission denied"), "", "--version query failed: launch_failure: cannot launch command (check executable, cwd and permissions): permission denied"},
		{"timeout", failed(model.FailureTimeout, "command exceeded its configured timeout"), "", "--version query failed: timeout: command exceeded its configured timeout (10 s)"},
		{"output limit", failed(model.FailureOutputLimit, "command exceeded the stdout or stderr capture limit"), "", "--version query failed: output_limit: command exceeded the stdout or stderr capture limit"},
		{"I/O failure", failed(model.FailureIO, "output pipes remained open after command termination"), "", "--version query failed: io: output pipes remained open after command termination"},
		{"no normal exit", model.ProcessObservation{Started: true}, "", "--version query failed: no normal exit was observed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			version, versionError := versionLine(c.observation, qemuVersionTimeoutSeconds)
			if version != c.version || versionError != c.versionError {
				t.Fatalf("got %q %q, want %q %q", version, versionError, c.version, c.versionError)
			}
		})
	}
}

func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("version query process %d outlived its query: %v", pid, err)
	}
}
