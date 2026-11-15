// SPDX-License-Identifier: Apache-2.0

// Package cli owns command-line parsing and report destinations. Execution uses
// the same orchestration engine and report model as the public Go API.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/buildinfo"
	"github.com/firmdura/targetcheck/internal/evaluate"
	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/orchestrate"
	"github.com/firmdura/targetcheck/pkg/targetcheck"
)

const help = `Usage:
  targetcheck check --manifest FILE --root DIR --output NEW_DIR
  targetcheck version

Compare the declared baseline and candidate tests on Linux (native or ARM32
QEMU). Both variants are preflighted before commands run. Build/test programs
are trusted arbitrary programs; TargetCheck is not a sandbox.

check writes report.json, report.md and restricted-permission logs to a new
output directory outside both variant workspaces. Existing destinations are
refused. Malformed-manifest failure reports require output outside --root.
An INCOMPLETE marker identifies interrupted or failed persistence.
Raw logs may contain sensitive data. No tools or source are downloaded.

Exit codes: 0 expectations matched; 1 behavior mismatch; 2 invalid input,
infrastructure or report-write failure; 130 cancellation, after stopping the
running command and finalizing the reports. SIGINT, SIGTERM and SIGHUP cancel
(exit 130) unless the signal was ignored when targetcheck started (e.g.
nohup); only SIGINT and SIGHUP can stay ignored, so SIGTERM always cancels.
Exits 2 and 130 print one stderr diagnostic, "targetcheck: error: KIND:
MESSAGE" or "targetcheck: cancelled: MESSAGE", with the report's failure kind
and message.
`

type runFunc func(context.Context, model.Spec, model.Options, func() error) (model.Report, error)

// cancellation is the failure of every run its caller cancelled.
var cancellation = model.Failure{Kind: model.FailureCancelled, Message: "run cancelled by caller"}

// Run handles one CLI invocation. It does not call os.Exit or install handlers;
// main supplies a signal-aware context. stdout/stderr write errors return 2.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr, (orchestrate.Engine{}).Run)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, execute runFunc) int {
	if stdout == nil || stderr == nil {
		return 2
	}
	if len(args) == 0 {
		return usage(stderr, "a command is required")
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return usage(stderr, "help takes no arguments")
		}
		if _, err := io.WriteString(stdout, help); err != nil {
			return 2
		}
		return 0
	case "version":
		if len(args) != 1 {
			return usage(stderr, "version takes no arguments")
		}
		info := buildinfo.Info()
		if _, err := fmt.Fprintf(stdout, "targetcheck %s commit=%s go=%s\n", info.Version, info.Commit, info.GoVersion); err != nil {
			return 2
		}
		return 0
	case "check":
		return check(ctx, args[1:], stdout, stderr, execute)
	default:
		return usage(stderr, "unknown command "+args[0])
	}
}

func usage(w io.Writer, message string) int {
	if writeDiagnostic(w, model.OutcomeError, model.Failure{Kind: model.FailureInvalidInput, Message: message}) == nil {
		io.WriteString(w, help)
	}
	return 2
}

func check(ctx context.Context, args []string, stdout, stderr io.Writer, execute runFunc) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifest := fs.String("manifest", "", "strict JSON manifest")
	rootFlag := fs.String("root", "", "input workspace")
	outputFlag := fs.String("output", "", "new report directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err = io.WriteString(stdout, help); err != nil {
				return 2
			}
			return 0
		}
		return usage(stderr, err.Error())
	}
	if fs.NArg() != 0 || *manifest == "" || *rootFlag == "" || *outputFlag == "" {
		return usage(stderr, "check requires --manifest, --root and --output, with no positional arguments")
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return withoutReport(ctx, stderr, nil, model.Failure{Kind: model.FailureIO, Message: err.Error()})
	}
	output, err := filepath.Abs(*outputFlag)
	if err != nil {
		return withoutReport(ctx, stderr, nil, model.Failure{Kind: model.FailureIO, Message: err.Error()})
	}
	var spec model.Spec
	f, err := os.OpenFile(*manifest, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		info, statErr := f.Stat()
		if statErr != nil {
			err = statErr
		} else if !info.Mode().IsRegular() {
			err = errors.New("manifest must be an ordinary file")
		} else {
			spec, err = targetcheck.Load(f)
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	loaded := err == nil
	var report model.Report
	var runErr error
	if !loaded {
		// The loader already classified its own error; keep its kind and message
		// instead of repeating its category inside this one.
		failure := model.Failure{Kind: model.FailureInvalidInput, Message: "load manifest: " + err.Error()}
		var loadErr *model.RunError
		if errors.As(err, &loadErr) {
			failure = model.Failure{Kind: loadErr.Failure.Kind, Message: "load manifest: " + loadErr.Failure.Message}
		}
		runErr = &model.RunError{Kind: model.ErrorInvalidInput, Failure: failure, Cause: err}
		if ctx != nil && ctx.Err() != nil {
			runErr = &model.RunError{Kind: model.ErrorCancelled, Failure: cancellation, Cause: ctx.Err()}
		}
		report = orchestrate.FailureReport(runErr)
	}
	// Even early failures may write reports only to an independently safe output.
	if outputErr := validateDestination(output, root, spec, loaded); outputErr != nil {
		return withoutReport(ctx, stderr, runFailure(runErr), model.Failure{Kind: model.FailureInvalidInput, Message: outputErr.Error()})
	}
	var destination *outputDir
	attempted := false
	reserve := func() error { attempted = true; var e error; destination, e = reserveOutput(output); return e }
	if loaded {
		report, runErr = execute(ctx, spec, model.Options{Root: root, OutputDir: output}, reserve)
	}
	if destination == nil && !attempted {
		if err = reserve(); err != nil {
			return withoutReport(ctx, stderr, runFailure(runErr), model.Failure{Kind: model.FailureIO, Message: err.Error()})
		}
	}
	if destination == nil {
		// The run itself reports the failed reservation of its destination.
		failure := model.Failure{Kind: model.FailureIO, Message: "output reservation failed"}
		if f := runFailure(runErr); f != nil {
			failure = *f
		} else if runErr != nil {
			failure.Message = runErr.Error()
		}
		return withoutReport(ctx, stderr, nil, failure)
	}
	defer destination.Close()
	markCancelled(ctx, &report)
	if err = persist(destination, &report); err != nil {
		code := 2
		if markCancelled(ctx, &report) {
			_ = destination.reports(report)
		}
		var notes []string
		if report.Outcome == model.OutcomeCancelled {
			code = 130
			notes = append(notes, "output persistence failed: "+consoleText(err.Error()))
		}
		writeDiagnostic(stderr, report.Outcome, reportFailure(report), append(notes, "incomplete output at "+strconv.QuoteToASCII(output))...)
		return code
	}
	if markCancelled(ctx, &report) {
		if err = destination.cancelledReports(report); err != nil {
			writeDiagnostic(stderr, report.Outcome, reportFailure(report), "output persistence failed: "+consoleText(err.Error()), "incomplete output at "+strconv.QuoteToASCII(output))
			return 130
		}
	}
	code := evaluate.ExitCode(report)
	if report.Outcome == model.OutcomeError || report.Outcome == model.OutcomeCancelled {
		if err = writeDiagnostic(stderr, report.Outcome, reportFailure(report)); err != nil {
			if code == 130 {
				return 130
			}
			return 2
		}
	}
	if _, err = fmt.Fprintf(stdout, "targetcheck: %s\nJSON: %q\nMarkdown: %q\n", report.Outcome, filepath.Join(output, "report.json"), filepath.Join(output, "report.md")); err != nil {
		if code == 130 {
			return 130
		}
		return 2
	}
	return code
}

// runFailure returns the failure a typed run error records, if any.
func runFailure(err error) *model.Failure {
	var runErr *model.RunError
	if errors.As(err, &runErr) {
		f := runErr.Failure
		return &f
	}
	return nil
}

func reportFailure(r model.Report) model.Failure {
	if r.Failure == nil {
		return model.Failure{Kind: model.FailureIO, Message: "report records no failure"}
	}
	return *r.Failure
}

// withoutReport writes the diagnostic of a run that ends before any report was
// written and returns 130 after cancellation, otherwise 2. run is the failure a
// report would have recorded, when the run got that far; reason says why no
// report exists and becomes the failure itself when run is nil. Cancellation
// outranks both, as in reports, and keeps the missing-report reason.
func withoutReport(ctx context.Context, w io.Writer, run *model.Failure, reason model.Failure) int {
	failure, notes := reason, []string(nil)
	if run != nil {
		failure = *run
		if reason.Message != "" {
			notes = append(notes, "no report written: "+consoleText(reason.Message))
		}
	}
	if ctx != nil && ctx.Err() != nil || failure.Kind == model.FailureCancelled {
		if notes == nil && failure.Kind != model.FailureCancelled && failure.Message != "" {
			notes = append(notes, "no report written: "+consoleText(failure.Message))
		}
		writeDiagnostic(w, model.OutcomeCancelled, cancellation, notes...)
		return 130
	}
	writeDiagnostic(w, model.OutcomeError, failure, notes...)
	return 2
}

// writeDiagnostic writes the single stderr line of an unsuccessful run,
//
//	targetcheck: OUTCOME: KIND: MESSAGE[; NOTE...]
//
// where OUTCOME is error (exit 2) or cancelled (exit 130), KIND is the failure
// kind, omitted when it would repeat OUTCOME, and MESSAGE is the failure
// message. Kind and message are escaped here; notes must already be escaped.
func writeDiagnostic(w io.Writer, outcome model.Outcome, f model.Failure, notes ...string) error {
	var b strings.Builder
	b.WriteString("targetcheck: " + consoleText(string(outcome)))
	if f.Kind != "" && string(f.Kind) != string(outcome) {
		b.WriteString(": " + consoleText(string(f.Kind)))
	}
	b.WriteString(": " + consoleText(f.Message))
	for _, note := range notes {
		b.WriteString("; " + note)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// consoleText keeps untrusted text on one printable-ASCII line: backslashes,
// control characters, non-ASCII runes and invalid UTF-8 bytes use Go escapes
// (\\, \n, \x1b, \u00e9, \xff), exactly as strconv.QuoteToASCII writes them;
// other printable ASCII, including quotes, is unchanged.
func consoleText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case r >= ' ' && r <= '~':
			b.WriteRune(r)
		default:
			quoted := strconv.QuoteRuneToASCII(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		}
		i += size
	}
	return b.String()
}

// markCancelled reports a late cancellation without discarding observations.
func markCancelled(ctx context.Context, r *model.Report) bool {
	if ctx == nil || ctx.Err() == nil || r.Outcome == model.OutcomeCancelled {
		return false
	}
	r.Outcome = model.OutcomeCancelled
	failure := cancellation
	r.Failure = &failure
	return true
}
