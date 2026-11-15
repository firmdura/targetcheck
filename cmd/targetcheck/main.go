// SPDX-License-Identifier: Apache-2.0

// Command targetcheck runs the declared baseline and candidate component
// regression tests of a JSON manifest on a Linux host, natively or under ARM32
// QEMU user mode, and writes the evidence to a new directory.
//
// Usage:
//
//	targetcheck check --manifest FILE --root DIR --output NEW_DIR
//	targetcheck version
//
// check preflights both variants before any command runs, then writes
// report.json (authoritative), report.md and per-step stdout/stderr logs into
// NEW_DIR, which must not exist yet. It exits 0 when every declared expectation
// matched, 1 when a test expectation mismatched, 2 for invalid input, build or
// infrastructure failures and report-write failures, and 130 when cancelled.
// SIGINT, SIGTERM and SIGHUP cancel (exit 130) unless the signal was ignored
// when targetcheck started (e.g. nohup). A closing terminal sends SIGHUP. Go
// keeps an inherited ignore only for SIGINT and SIGHUP, so SIGTERM always
// cancels. Cancellation stops the running command's process group and finalizes
// both reports first. Exits 2 and 130 print one stderr diagnostic line,
// "targetcheck: error: KIND: MESSAGE" or "targetcheck: cancelled: MESSAGE",
// with the report's failure kind and escaped message. Build and test commands
// are trusted programs that run with the caller's permissions; targetcheck is
// not a sandbox. The Go package github.com/firmdura/targetcheck/pkg/targetcheck
// provides the same checks as a library.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/firmdura/targetcheck/internal/cli"
)

func main() {
	ctx, stop := context.Background(), func() {}
	// Without any signal to handle, NotifyContext would relay every signal.
	if signals := cancellationSignals(); len(signals) > 0 {
		ctx, stop = signal.NotifyContext(ctx, signals...)
	}
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// cancellationSignals lists the signals that cancel a run: SIGINT, SIGTERM and
// SIGHUP, which a closing terminal sends, so that command process groups are
// stopped instead of outliving the CLI. A signal that was already ignored when
// the CLI started stays ignored, as nohup intends for SIGHUP and a
// non-interactive shell for the SIGINT of a background job; handling it would
// re-enable it. Go keeps an inherited ignore only for SIGHUP and SIGINT.
func cancellationSignals() []os.Signal {
	var signals []os.Signal
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			signals = append(signals, sig)
		}
	}
	return signals
}
