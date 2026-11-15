//go:build linux

// SPDX-License-Identifier: Apache-2.0

package execute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/firmdura/targetcheck/internal/execute"
	"github.com/firmdura/targetcheck/internal/model"
)

var helper string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "targetcheck-executor-helper-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	helper = filepath.Join(dir, "process helper")
	cmd := exec.Command("gcc", "-std=gnu11", "-O2", "-Wall", "-Wextra", "-Werror", "-pthread", "testdata/helper.c", "-o", helper)
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "compile mandatory real process fixture: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func request(t *testing.T, mode string, args ...string) model.CommandRequest {
	t.Helper()
	return model.CommandRequest{
		Variant: model.Baseline, Kind: model.CheckTest, ID: "test",
		Target: model.TargetInfo{Kind: model.TargetNative},
		Argv:   append([]string{"./artifact with spaces"}, args...), ExecutablePath: helper,
		AbsoluteCWD: t.TempDir(), RelativeCWD: "baseline", TimeoutSeconds: 5,
		Env: map[string]string{"TARGETCHECK_HELPER_MODE": mode},
	}
}
func wantFailure(t *testing.T, got model.ProcessObservation, kind model.FailureKind) {
	t.Helper()
	if got.Failure == nil || got.Failure.Kind != kind || got.ExitCode != nil {
		t.Fatalf("want failure %s and no normal exit, got %+v", kind, got)
	}
}
func wantExit(t *testing.T, got model.ProcessObservation, code int) {
	t.Helper()
	if !got.Started || got.Failure != nil || got.ExitCode == nil || *got.ExitCode != code || got.Signal != "" {
		t.Fatalf("want normal exit %d, got %+v", code, got)
	}
}
func waitReady(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				pids := make([]int, 2)
				for i, s := range fields {
					pids[i], err = strconv.Atoi(s)
					if err != nil {
						t.Fatal(err)
					}
				}
				return pids
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("real subprocess did not become ready")
	return nil
}
func assertStopped(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for _, pid := range pids {
		if pid == 0 {
			continue
		}
		for {
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			// Grandchildren may briefly be zombies awaiting their system reaper;
			// they cannot run or retain pipe descriptors. We reap our direct child.
			dead := os.IsNotExist(err)
			if err == nil {
				end := strings.LastIndexByte(string(data), ')')
				dead = end >= 0 && strings.HasPrefix(string(data)[end+1:], " Z")
			}
			if dead {
				break
			}
			if time.Now().After(deadline) {
				syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("helper %d remains alive after Execute returned: %s", pid, data)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(pids[0], &status, syscall.WNOHANG, nil); err != syscall.ECHILD {
		t.Fatalf("direct child was not reaped: %v", err)
	}
}

func TestLiteralArgvCwdEnvironmentAndNoGlobalMutation(t *testing.T) {
	t.Setenv("TARGETCHECK_TEST_VALUE", "inherited-secret")
	beforeCWD, _ := os.Getwd()
	beforeEnv := os.Environ()
	sort.Strings(beforeEnv)
	args := []string{"a b", "$(touch should-not-exist)", ";echo bad", "*", "", "'quoted'", "a\nb"}
	r := request(t, "args", args...)
	r.Env["TARGETCHECK_TEST_VALUE"] = "overridden-secret"
	got := execute.Execute(context.Background(), r)
	wantExit(t, got, 0)
	var expected strings.Builder
	for _, arg := range r.Argv {
		fmt.Fprintf(&expected, "%d:%s\n", len(arg), arg)
	}
	fmt.Fprintf(&expected, "cwd:%s\nenv:overridden-secret\n", r.AbsoluteCWD)
	if string(got.Stdout.Data) != expected.String() || got.Stdout.Bytes != int64(expected.Len()) {
		t.Fatalf("literal argv/cwd/env lost: %q", got.Stdout.Data)
	}
	if !reflect.DeepEqual(got.Argv, r.Argv) || got.CWD != "baseline" {
		t.Fatalf("observation rewrote argv/cwd: %+v", got)
	}
	if !reflect.DeepEqual(got.EnvNames, []string{"TARGETCHECK_HELPER_MODE", "TARGETCHECK_TEST_VALUE"}) {
		t.Fatal(got.EnvNames)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"inherited-secret", "overridden-secret", r.AbsoluteCWD} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("execution-only value leaked in observation: %s", encoded)
		}
	}
	afterCWD, _ := os.Getwd()
	afterEnv := os.Environ()
	sort.Strings(afterEnv)
	if beforeCWD != afterCWD || !reflect.DeepEqual(beforeEnv, afterEnv) || r.Env["TARGETCHECK_TEST_VALUE"] != "overridden-secret" {
		t.Fatal("caller cwd/env or request mutated")
	}
	if _, err := os.Stat(filepath.Join(r.AbsoluteCWD, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("shell expansion occurred")
	}
}
func TestNormalExitsAndLaunchFailures(t *testing.T) {
	for _, code := range []int{0, 1, 127} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			r := request(t, "exit", strconv.Itoa(code))
			wantExit(t, execute.Execute(context.Background(), r), code)
		})
	}
	for _, which := range []string{"native", "qemu"} {
		t.Run(which, func(t *testing.T) {
			r := request(t, "exit", "0")
			if which == "native" {
				r.ExecutablePath = "/nonexistent-targetcheck-command"
			} else {
				r.Target = model.TargetInfo{Kind: model.TargetQEMUARM, Executable: "/nonexistent-qemu", Sysroot: "/sysroot"}
			}
			got := execute.Execute(context.Background(), r)
			wantFailure(t, got, model.FailureLaunch)
			if got.Started {
				t.Fatal("launch failure marked started")
			}
		})
	}
}
func TestLaunchDiagnosticsKeepWorkspacePathsExecutionOnly(t *testing.T) {
	r := request(t, "exit", "0")
	r.ExecutablePath = filepath.Join(r.AbsoluteCWD, "missing-artifact")
	got := execute.Execute(context.Background(), r)
	wantFailure(t, got, model.FailureLaunch)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(r.AbsoluteCWD)) {
		t.Fatalf("absolute workspace path leaked: %s", encoded)
	}
}

func TestSignalIsNotNormalExit(t *testing.T) {
	got := execute.Execute(context.Background(), request(t, "signal"))
	wantFailure(t, got, model.FailureSignal)
	if !got.Started || got.Signal != "terminated" {
		t.Fatalf("signal lost: %+v", got)
	}
}
func TestIndependentCapturesAndExactLimit(t *testing.T) {
	got := execute.Execute(context.Background(), request(t, "both"))
	wantExit(t, got, 0)
	if !bytes.Equal(got.Stdout.Data, bytes.Repeat([]byte("O"), 65536)) || !bytes.Equal(got.Stderr.Data, bytes.Repeat([]byte("E"), 65536)) {
		t.Fatal("concurrent streams lost or mixed data")
	}
	got = execute.Execute(context.Background(), request(t, "exact"))
	wantExit(t, got, 0)
	if len(got.Stdout.Data) != model.MaxCaptureBytes || got.Stdout.Bytes != model.MaxCaptureBytes || got.Stdout.Truncated {
		t.Fatal("exact capture limit truncated or miscounted")
	}
}
func TestOverflowStopsEachStream(t *testing.T) {
	for _, mode := range []string{"flood-out", "flood-err"} {
		t.Run(mode, func(t *testing.T) {
			start := time.Now()
			got := execute.Execute(context.Background(), request(t, mode))
			wantFailure(t, got, model.FailureOutputLimit)
			if time.Since(start) > 2*time.Second {
				t.Fatal("overflow did not terminate promptly")
			}
			capture := got.Stdout
			if mode == "flood-err" {
				capture = got.Stderr
			}
			if !capture.Truncated || len(capture.Data) != model.MaxCaptureBytes || capture.Bytes <= model.MaxCaptureBytes {
				t.Fatalf("unfaithful overflow capture: len=%d bytes=%d truncated=%t", len(capture.Data), capture.Bytes, capture.Truncated)
			}
			if len(got.Stdout.Data) > model.MaxCaptureBytes || len(got.Stderr.Data) > model.MaxCaptureBytes {
				t.Fatal("capture cap exceeded")
			}
		})
	}
}
func TestQEMUArgvAndHostBuildBypass(t *testing.T) {
	r := request(t, "args", "arg with spaces")
	r.Target = model.TargetInfo{Kind: model.TargetQEMUARM, Executable: helper, Sysroot: "/sysroot with spaces"}
	got := execute.Execute(context.Background(), r)
	wantExit(t, got, 0)
	want := append([]string{helper, "-L", r.Target.Sysroot}, r.Argv...)
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("bad QEMU argv: %q", got.Argv)
	}
	var prefix strings.Builder
	for _, s := range want {
		fmt.Fprintf(&prefix, "%d:%s\n", len(s), s)
	}
	if !strings.HasPrefix(string(got.Stdout.Data), prefix.String()) {
		t.Fatalf("QEMU launch argv differs from recorded argv: %q", got.Stdout.Data)
	}
	r.Kind = model.CheckBuild
	r.Target.Executable = "/must-not-launch-qemu"
	got = execute.Execute(context.Background(), r)
	wantExit(t, got, 0)
	if !reflect.DeepEqual(got.Argv, r.Argv) {
		t.Fatal("host build was QEMU-wrapped")
	}
}
func TestCancellationBeforeLaunchAndInvalidRequest(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	r := request(t, "mark", marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := execute.Execute(ctx, r)
	wantFailure(t, got, model.FailureCancelled)
	if got.Started {
		t.Fatal("cancelled request started")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled request ran")
	}
	for _, bad := range []func(*model.CommandRequest){func(r *model.CommandRequest) { r.Argv = nil }, func(r *model.CommandRequest) { r.TimeoutSeconds = 0 }, func(r *model.CommandRequest) { r.Kind = "invalid" }, func(r *model.CommandRequest) { r.AbsoluteCWD = "relative" }} {
		r2 := r
		bad(&r2)
		got = execute.Execute(context.Background(), r2)
		wantFailure(t, got, model.FailureInvalidInput)
		if got.Started {
			t.Fatal("invalid request ran")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid request ran")
	}
}
func TestTimeoutCancellationAndDescendantsRepeated(t *testing.T) {
	before := runtime.NumGoroutine()
	for _, mode := range []string{"sleep", "child"} {
		for _, stop := range []string{"timeout", "cancel"} {
			for iteration := 0; iteration < 3; iteration++ {
				t.Run(fmt.Sprintf("%s-%s-%d", mode, stop, iteration), func(t *testing.T) {
					marker := filepath.Join(t.TempDir(), "pids")
					r := request(t, mode, marker)
					r.TimeoutSeconds = 1
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan model.ProcessObservation, 1)
					start := time.Now()
					go func() { done <- execute.Execute(ctx, r) }()
					pids := waitReady(t, marker)
					if stop == "cancel" {
						cancel()
					}
					var got model.ProcessObservation
					select {
					case got = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("timeout plus two-second completion bound exceeded")
					}
					kind := model.FailureTimeout
					if stop == "cancel" {
						kind = model.FailureCancelled
					}
					wantFailure(t, got, kind)
					if time.Since(start) > 3*time.Second {
						t.Fatal("configured timeout plus two-second bound exceeded")
					}
					assertStopped(t, pids)
				})
			}
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines leaked: before=%d after=%d", before, after)
	}
}
func TestCallerDeadlineIsCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pids")
	r := request(t, "sleep", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got := execute.Execute(ctx, r)
	wantFailure(t, got, model.FailureCancelled)
	if !got.Started {
		t.Fatal("fixture never launched")
	}
	assertStopped(t, waitReady(t, marker))
}

func TestGracefulTERMAndParentExitDescendantCleanup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pids")
	termfile := filepath.Join(t.TempDir(), "term")
	r := request(t, "grace", marker, termfile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan model.ProcessObservation, 1)
	go func() { done <- execute.Execute(ctx, r) }()
	pids := waitReady(t, marker)
	cancel()
	wantFailure(t, <-done, model.FailureCancelled)
	assertStopped(t, pids)
	if data, err := os.ReadFile(termfile); err != nil || string(data) != "term" {
		t.Fatal("TERM grace was not offered before KILL", err)
	}
	marker = filepath.Join(t.TempDir(), "exited-pids")
	got := execute.Execute(context.Background(), request(t, "parent-exit", marker))
	wantExit(t, got, 0)
	assertStopped(t, waitReady(t, marker))
}
func TestConcurrentCallsKeepIndependentEnvironment(t *testing.T) {
	r1 := request(t, "args")
	r2 := request(t, "args")
	r1.Env["TARGETCHECK_TEST_VALUE"] = "first"
	r2.Env["TARGETCHECK_TEST_VALUE"] = "second"
	done := make(chan model.ProcessObservation, 2)
	go func() { done <- execute.Execute(context.Background(), r1) }()
	go func() { done <- execute.Execute(context.Background(), r2) }()
	seen := map[string]bool{}
	for range 2 {
		got := <-done
		wantExit(t, got, 0)
		seen[string(got.Stdout.Data)] = true
	}
	for _, r := range []model.CommandRequest{r1, r2} {
		key := fmt.Sprintf("%d:%s\ncwd:%s\nenv:%s\n", len(r.Argv[0]), r.Argv[0], r.AbsoluteCWD, r.Env["TARGETCHECK_TEST_VALUE"])
		if !seen[key] {
			t.Fatalf("concurrent state mixed: %v", seen)
		}
	}
}
