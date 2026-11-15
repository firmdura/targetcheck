//go:build linux

// SPDX-License-Identifier: Apache-2.0

package execute

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/firmdura/targetcheck/internal/model"
)

const (
	terminationGrace = 250 * time.Millisecond
	drainGrace       = 500 * time.Millisecond
)

// Executor implements model.Executor without shared mutable run state.
type Executor struct{}

func (Executor) Execute(ctx context.Context, request model.CommandRequest) model.ProcessObservation {
	return Execute(ctx, request)
}

var _ model.Executor = Executor{}

// Execute launches a preflighted request directly, without shell interpretation.
// Cancellation of the caller context takes precedence over other observations.
func Execute(ctx context.Context, request model.CommandRequest) (observation model.ProcessObservation) {
	begin := time.Now()
	defer func() { observation.DurationMillis = time.Since(begin).Milliseconds() }()
	observation.CWD = request.RelativeCWD
	observation.EnvNames = make([]string, 0, len(request.Env))
	for name := range request.Env {
		observation.EnvNames = append(observation.EnvNames, name)
	}
	sort.Strings(observation.EnvNames)
	observation.Argv = append([]string{}, request.Argv...)
	path := request.ExecutablePath
	if request.Kind == model.CheckTest && request.Target.Kind == model.TargetQEMUARM {
		path = request.Target.Executable
		observation.Argv = append([]string{path, "-L", request.Target.Sysroot}, observation.Argv...)
	}
	fail := func(kind model.FailureKind, message string) {
		observation.Failure = &model.Failure{Kind: kind, Message: message}
	}
	if ctx == nil {
		fail(model.FailureInvalidInput, "command context is required")
		return
	}
	if ctx.Err() != nil {
		fail(model.FailureCancelled, "command cancelled before launch")
		return
	}
	if len(request.Argv) == 0 || request.Argv[0] == "" || path == "" || !filepath.IsAbs(request.AbsoluteCWD) || request.TimeoutSeconds < model.MinTimeoutSeconds || request.TimeoutSeconds > model.MaxTimeoutSeconds {
		fail(model.FailureInvalidInput, "command requires argv, a launch path, an absolute cwd and a timeout in 1..3600 seconds")
		return
	}
	if request.Kind != model.CheckBuild && request.Kind != model.CheckTest {
		fail(model.FailureInvalidInput, "command kind must be build or test")
		return
	}
	if request.Kind == model.CheckTest && request.Target.Kind != model.TargetNative && request.Target.Kind != model.TargetQEMUARM {
		fail(model.FailureInvalidInput, "unsupported test target")
		return
	}
	if request.Kind == model.CheckTest && request.Target.Kind == model.TargetQEMUARM && request.Target.Sysroot == "" {
		fail(model.FailureInvalidInput, "QEMU test requires a sysroot")
		return
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		fail(model.FailureIO, "cannot create stdout pipe: "+err.Error())
		return
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		fail(model.FailureIO, "cannot create stderr pipe: "+err.Error())
		return
	}
	defer stderrR.Close()
	defer stderrW.Close()
	cmd := &exec.Cmd{
		Path: path, Args: observation.Argv, Dir: request.AbsoluteCWD,
		Env: childEnvironment(request.Env), Stdout: stdoutW, Stderr: stderrW,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	if ctx.Err() != nil {
		fail(model.FailureCancelled, "command cancelled before launch")
		return
	}
	if err := cmd.Start(); err != nil {
		fail(model.FailureLaunch, "cannot launch command (check executable, cwd and permissions): "+launchError(err))
		if ctx.Err() != nil {
			fail(model.FailureCancelled, "command cancelled during launch")
		}
		return
	}
	observation.Started = true
	// Only the child and its descendants now own the write ends.
	stdoutW.Close()
	stderrW.Close()
	overflow := make(chan struct{}, 1)
	out := &captureWriter{overflow: overflow}
	errout := &captureWriter{overflow: overflow}
	readDone := make(chan error, 2)
	go func() { _, err := io.Copy(out, stdoutR); readDone <- err }()
	go func() { _, err := io.Copy(errout, stderrR); readDone <- err }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	timer := time.NewTimer(time.Duration(request.TimeoutSeconds) * time.Second)
	defer timer.Stop()
	var waitErr error
	waited := false
	select {
	case waitErr = <-waitDone:
		waited = true
	case <-ctx.Done():
		fail(model.FailureCancelled, "command cancelled by caller")
	case <-timer.C:
		fail(model.FailureTimeout, "command exceeded its configured timeout")
	case <-overflow:
		fail(model.FailureOutputLimit, "command exceeded the stdout or stderr capture limit")
	}
	// Also clean up ordinary descendants after the direct program exits. Some
	// descendants may ignore TERM or keep a pipe open after their parent exits.
	if err := stopGroup(cmd.Process.Pid); err != nil && observation.Failure == nil {
		fail(model.FailureIO, "cannot stop command process group: "+err.Error())
	}
	if !waited {
		waitErr = <-waitDone // KILL has been sent; reap the direct child.
	}
	// EOF normally arrives immediately. Closing read ends after this bound also
	// prevents deliberately escaped descriptor holders from blocking the caller.
	drain := time.NewTimer(drainGrace)
	for i := 0; i < 2; i++ {
		select {
		case err := <-readDone:
			if err != nil && observation.Failure == nil {
				fail(model.FailureIO, "cannot read command output: "+err.Error())
			}
		case <-drain.C:
			stdoutR.Close()
			stderrR.Close()
			if observation.Failure == nil {
				fail(model.FailureIO, "output pipes remained open after command termination")
			}
			// Closing an os.File interrupts its outstanding pipe Read.
			for ; i < 2; i++ {
				<-readDone
			}
		}
	}
	drain.Stop()
	observation.Stdout = out.capture
	observation.Stderr = errout.capture
	if observation.Failure == nil && (out.capture.Truncated || errout.capture.Truncated) {
		fail(model.FailureOutputLimit, "command exceeded the stdout or stderr capture limit")
	}
	if cmd.ProcessState != nil {
		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if ok && status.Signaled() {
			observation.Signal = status.Signal().String()
			if observation.Failure == nil {
				fail(model.FailureSignal, "command terminated by signal "+observation.Signal)
			}
		} else if observation.Failure == nil && cmd.ProcessState.Exited() {
			exit := cmd.ProcessState.ExitCode()
			observation.ExitCode = &exit
		}
	}
	if observation.Failure == nil && waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			fail(model.FailureIO, "cannot wait for command: "+waitErr.Error())
		}
	}
	if ctx.Err() != nil {
		fail(model.FailureCancelled, "command cancelled by caller")
		observation.ExitCode = nil
	}
	return
}

// A separate writer per pipe means only the reader goroutine mutates each
// capture; Execute reads it after both readers have completed.
type captureWriter struct {
	capture  model.Capture
	overflow chan<- struct{}
}

func (w *captureWriter) Write(data []byte) (int, error) {
	w.capture.Bytes += int64(len(data))
	remaining := model.MaxCaptureBytes - len(w.capture.Data)
	if remaining > len(data) {
		remaining = len(data)
	}
	w.capture.Data = append(w.capture.Data, data[:remaining]...)
	if remaining < len(data) {
		w.capture.Truncated = true
		select {
		case w.overflow <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

func childEnvironment(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; !replaced {
			env = append(env, entry)
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+overrides[name])
	}
	return env
}

func stopGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	// Always escalate after grace even if the direct child exited: a descendant
	// may ignore TERM. No process-global signal handlers or subreaper are used.
	time.Sleep(terminationGrace)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// PathError includes absolute workspace paths. Keep the underlying OS cause
// actionable without inserting execution-only path prefixes in report messages.
func launchError(err error) string {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Err.Error()
	}
	return err.Error()
}
