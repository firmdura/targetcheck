// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/orchestrate"
	"github.com/firmdura/targetcheck/internal/plan"
)

func temp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func loadReport(t *testing.T, path string) model.Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r model.Report
	if err = json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func fixture(t *testing.T) model.Report {
	t.Helper()
	return loadReport(t, "../render/testdata/matched-negative.json")
}
func manifestFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/contracts/valid/native.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	write(t, path, data)
	return path
}
func args(manifest, root, output string) []string {
	return []string{"check", "--manifest", manifest, "--root", root, "--output", output}
}

func TestUsageAndVersion(t *testing.T) {
	for _, tt := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"--help"}, 0, "Usage:", ""}, {[]string{"check", "--help"}, 0, "Exit codes:", ""}, {[]string{"version"}, 0, "commit=", ""}, {nil, 2, "", "a command is required"}, {[]string{"check"}, 2, "", "requires --manifest"}, {[]string{"version", "extra"}, 2, "", "takes no arguments"}, {[]string{"unknown"}, 2, "", "unknown command"}, {[]string{"check", "--bogus"}, 2, "", "flag provided"},
	} {
		var out, errout bytes.Buffer
		if code := Run(context.Background(), tt.args, &out, &errout); code != tt.code {
			t.Fatalf("%v code %d: %s", tt.args, code, errout.String())
		}
		if !strings.Contains(out.String(), tt.stdout) || !strings.Contains(errout.String(), tt.stderr) {
			t.Fatalf("%v out %q err %q", tt.args, out.String(), errout.String())
		}
	}
	for _, command := range []string{"help", "version"} {
		if code := Run(context.Background(), []string{command}, badWriter{}, io.Discard); code != 2 {
			t.Fatalf("broken stdout returned %d", code)
		}
	}
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("closed console") }

func TestMalformedManifestWritesFaithfulEarlyReports(t *testing.T) {
	dir := temp(t)
	root := filepath.Join(dir, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "bad.json")
	write(t, manifest, []byte(`{"schema_version":1,"schema_version":1}`))
	output := filepath.Join(dir, "report")
	calls := 0
	runner := func(context.Context, model.Spec, model.Options, func() error) (model.Report, error) {
		calls++
		panic("malformed manifest reached execution")
	}
	var out, errout bytes.Buffer
	if code := run(context.Background(), args(manifest, root, output), &out, &errout, runner); code != 2 {
		t.Fatalf("code %d: %s", code, errout.String())
	}
	if calls != 0 {
		t.Fatal("execution called")
	}
	r := loadReport(t, filepath.Join(output, "report.json"))
	if r.Outcome != model.OutcomeError || r.Failure == nil || r.Failure.Kind != model.FailureInvalidInput || len(r.Variants) != 0 || r.Tool.Version == "" || r.Tool.Commit == "" || r.Tool.GoVersion == "" {
		t.Fatalf("early report %+v", r)
	}
	md, err := os.ReadFile(filepath.Join(output, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(md, []byte("error")) || !bytes.Contains(md, []byte("No declared execution coverage")) {
		t.Fatalf("bad markdown %s", md)
	}
	if _, err = os.Stat(filepath.Join(output, incomplete)); !os.IsNotExist(err) {
		t.Fatalf("completed failure report left marker: %v", err)
	}
	assertMode(t, output, 0700)
	assertMode(t, filepath.Join(output, "report.json"), 0600)
	assertMode(t, filepath.Join(output, "report.md"), 0600)
	output = filepath.Join(root, "unsafe")
	if code := run(context.Background(), args(manifest, root, output), io.Discard, io.Discard, runner); code != 2 {
		t.Fatal(code)
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("invalid declaration wrote inside input root")
	}
}

func TestSuccessfulPersistenceUsesObservedLogsAndSafeNames(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	output := filepath.Join(dir, "report")
	r := fixture(t)
	for i := range r.Variants {
		for j := range r.Variants[i].Tests {
			c := &r.Variants[i].Tests[j]
			c.ID = "../../<bad>\n`id`" + strconv.Itoa(c.Index)
			r.Declared[i].TestIDs[c.Index] = c.ID
			c.Observation.Stdout.Data = []byte("raw output\n")
			c.Observation.Stdout.Bytes = int64(len(c.Observation.Stdout.Data))
		}
	}
	if err := model.ValidateCoverage(r); err != nil {
		t.Fatalf("invalid sink-test fixture: %v", err)
	}
	runner := func(_ context.Context, _ model.Spec, o model.Options, reserve func() error) (model.Report, error) {
		if o.OutputDir != output {
			t.Fatal("wrong output option")
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			t.Fatal("destination reserved before preflight hook")
		}
		if err := reserve(); err != nil {
			t.Fatal(err)
		}
		return r, nil
	}
	var out, errout bytes.Buffer
	if code := run(context.Background(), args(manifest, dir, output), &out, &errout, runner); code != 0 {
		t.Fatalf("code %d err %s", code, errout.String())
	}
	if strings.Contains(out.String(), "raw output") {
		t.Fatal("raw log leaked to console")
	}
	actual := loadReport(t, filepath.Join(output, "report.json"))
	for _, v := range actual.Variants {
		for _, c := range v.Tests {
			if c.Observation == nil {
				continue
			}
			want := "logs/" + string(v.Name) + "-test-" + strconv.Itoa(c.Index) + ".stdout"
			if c.Observation.Stdout.Log != want {
				t.Fatalf("log %q", c.Observation.Stdout.Log)
			}
			data, err := os.ReadFile(filepath.Join(output, want))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "raw output\n" {
				t.Fatalf("log content %q", data)
			}
			assertMode(t, filepath.Join(output, want), 0600)
		}
	}
	if _, err := os.Stat(filepath.Join(output, incomplete)); !os.IsNotExist(err) {
		t.Fatalf("marker retained: %v", err)
	}
}

func TestWriteFailureReturnsTwoAndKeepsIncompleteEvidence(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	output := filepath.Join(dir, "report")
	runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
		if err := reserve(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(output, "report.md"), 0700); err != nil {
			t.Fatal(err)
		}
		return fixture(t), nil
	}
	var out, errout bytes.Buffer
	if code := run(context.Background(), args(manifest, dir, output), &out, &errout, runner); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errout.String(), "incomplete output") {
		t.Fatalf("diagnostic %q", errout.String())
	}
	if _, err := os.Stat(filepath.Join(output, incomplete)); err != nil {
		t.Fatal(err)
	}
	r := loadReport(t, filepath.Join(output, "report.json"))
	if r.Outcome != model.OutcomeError || r.Failure == nil || r.Failure.Kind != model.FailureIO {
		t.Fatalf("report lacks persistence failure: %+v", r)
	}
	info, err := os.Stat(filepath.Join(output, "report.md"))
	if err != nil || !info.IsDir() {
		t.Fatal("existing collision was deleted")
	}
}

func TestReservationCollisionNeverReusesExistingData(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	output := filepath.Join(dir, "report")
	runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(output, "keep"), []byte("user data"))
		err := reserve()
		if err == nil {
			t.Fatal("collision accepted")
		}
		return orchestrate.FailureReport(err), err
	}
	if code := run(context.Background(), args(manifest, dir, output), io.Discard, io.Discard, runner); code != 2 {
		t.Fatal(code)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("changed user destination: %v", entries)
	}
}

func TestDestinationBoundaries(t *testing.T) {
	dir := temp(t)
	root := filepath.Join(dir, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	spec := model.Spec{Variants: model.Variants{Baseline: model.Variant{CWD: "baseline"}, Candidate: model.Variant{CWD: "candidate"}}}
	if err := validateDestination(filepath.Join(root, "baseline", "out"), root, spec, true); err == nil {
		t.Fatal("inside baseline accepted")
	}
	if err := os.Mkdir(filepath.Join(dir, "existing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateDestination(filepath.Join(dir, "existing"), root, spec, true); err == nil {
		t.Fatal("existing accepted")
	}
	if err := os.Symlink(dir, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := validateDestination(filepath.Join(dir, "link", "out"), root, spec, true); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if err := validateDestination(filepath.Join(dir, "link"), root, spec, true); err == nil {
		t.Fatal("symlink destination accepted")
	}
	if err := validateDestination(filepath.Join(dir, "out"), filepath.Join(dir, "link"), spec, true); err == nil {
		t.Fatal("symlink workspace accepted")
	}
	if err := validateDestination(filepath.Join(dir, "missing", "out"), root, spec, true); err == nil {
		t.Fatal("missing parent accepted")
	}
}

func TestHeldOutputCannotWriteThroughReplacement(t *testing.T) {
	dir := temp(t)
	path := filepath.Join(dir, "report")
	o, err := reserveOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	moved := filepath.Join(dir, "moved")
	outside := filepath.Join(dir, "outside")
	if err = os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	r := fixture(t)
	if err = persist(o, &r); err == nil {
		t.Fatal("replacement accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote through replacement: %v", entries)
	}
	if _, err = os.Stat(filepath.Join(moved, incomplete)); err != nil {
		t.Fatal("missing incomplete marker")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode %o want %o", path, info.Mode().Perm(), want)
	}
}

func TestFinalizationFailureUpdatesBothReports(t *testing.T) {
	dir := temp(t)
	path := filepath.Join(dir, "report")
	o, err := reserveOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err = os.Remove(filepath.Join(path, incomplete)); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(path, incomplete), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, incomplete, "keep"), []byte("block marker removal"))
	r := fixture(t)
	if err = persist(o, &r); err == nil {
		t.Fatal("finalization failure accepted")
	}
	actual := loadReport(t, filepath.Join(path, "report.json"))
	if actual.Outcome != model.OutcomeError || actual.Failure == nil || actual.Failure.Kind != model.FailureIO {
		t.Fatalf("JSON contradicts finalization error: %+v", actual)
	}
	md, err := os.ReadFile(filepath.Join(path, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(md, []byte("output persistence failed")) {
		t.Fatalf("Markdown contradicts finalization error: %s", md)
	}
	if _, err = os.Stat(filepath.Join(path, incomplete, "keep")); err != nil {
		t.Fatal("marker data removed")
	}
}

func TestCancellationOutranksPersistenceFailure(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	output := filepath.Join(dir, "report")
	runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
		if err := reserve(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(output, "report.md"), 0700); err != nil {
			t.Fatal(err)
		}
		r := loadReport(t, "../render/testdata/cancelled-partial.json")
		return r, &model.RunError{Kind: model.ErrorCancelled, Failure: *r.Failure, Cause: context.Canceled}
	}
	if code := run(context.Background(), args(manifest, dir, output), io.Discard, io.Discard, runner); code != 130 {
		t.Fatalf("cancellation plus persistence failure code %d", code)
	}
	actual := loadReport(t, filepath.Join(output, "report.json"))
	if actual.Outcome != model.OutcomeCancelled || actual.Failure == nil || actual.Failure.Kind != model.FailureCancelled {
		t.Fatalf("lost cancellation: %+v", actual)
	}
	if _, err := os.Stat(filepath.Join(output, incomplete)); err != nil {
		t.Fatal(err)
	}
}

func TestUsageEscapesTerminalControls(t *testing.T) {
	var out, errout bytes.Buffer
	if code := Run(context.Background(), []string{"check", "--bad\nflag\x1b"}, &out, &errout); code != 2 {
		t.Fatal(code)
	}
	line := strings.SplitN(errout.String(), "\n", 2)[0]
	if line != `targetcheck: error: invalid_input: flag provided but not defined: -bad\nflag\x1b` {
		t.Fatalf("unsafe or unstable diagnostic %q", line)
	}
	if !strings.HasPrefix(errout.String(), line+"\nUsage:") {
		t.Fatalf("usage does not follow the diagnostic: %q", errout.String())
	}
}

func TestConsoleTextEscapesUntrustedText(t *testing.T) {
	for in, want := range map[string]string{
		`plain "quoted" text; with: punctuation`: `plain "quoted" text; with: punctuation`,
		"line\nbreak\r\ttab":                     `line\nbreak\r\ttab`,
		"\x1b[31mred\x7f":                        `\x1b[31mred\x7f`,
		`back\slash`:                             `back\\slash`,
		"caf\u00e9 \u202e \U0001F600":            `caf\u00e9 \u202e \U0001f600`,
		"invalid \xff\xfe bytes":                 `invalid \xff\xfe bytes`,
		"replacement \ufffd rune":                `replacement \ufffd rune`,
	} {
		got := consoleText(in)
		if got != want {
			t.Errorf("consoleText(%q) = %q, want %q", in, got, want)
		}
		quoted := strconv.QuoteToASCII(in)
		if unquoted := strings.ReplaceAll(quoted[1:len(quoted)-1], `\"`, `"`); unquoted != got {
			t.Errorf("consoleText(%q) = %q, QuoteToASCII escapes %q", in, got, unquoted)
		}
		for _, r := range got {
			if r < ' ' || r > '~' {
				t.Errorf("consoleText(%q) kept %q", in, r)
			}
		}
	}
}

// The single stderr diagnostic states the outcome, the specific failure kind
// and the message exactly once, from the same report the CLI writes.
func TestConsoleDiagnosticStatesOutcomeKindAndMessage(t *testing.T) {
	type result struct {
		code           int
		stdout, stderr string
		report         *model.Report
	}
	invoke := func(t *testing.T, ctx context.Context, manifest, root, output string, runner runFunc) result {
		t.Helper()
		var stdout, stderr bytes.Buffer
		r := result{code: run(ctx, args(manifest, root, output), &stdout, &stderr, runner)}
		r.stdout, r.stderr = stdout.String(), stderr.String()
		if data, err := os.ReadFile(filepath.Join(output, "report.json")); err == nil {
			var report model.Report
			if err = json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			r.report = &report
		}
		return r
	}
	returning := func(r model.Report, err error) runFunc {
		return func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
			if r.Outcome != model.OutcomeError || r.Failure.Kind != model.FailureMissingTool {
				if e := reserve(); e != nil {
					t.Fatal(e)
				}
			}
			return r, err
		}
	}
	golden := func(name string) (model.Report, error) {
		r := loadReport(t, "../render/testdata/"+name+".json")
		kind := model.ErrorInfrastructure
		if r.Outcome == model.OutcomeCancelled {
			kind = model.ErrorCancelled
		}
		return r, &model.RunError{Kind: kind, Failure: *r.Failure}
	}
	missingQEMU := &model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureMissingTool, Message: "QEMU executable is unavailable"}}
	untrusted := &model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureIO, Message: "baseline test \"t\\n\": cannot read command output: \x1b[2J\ncaf\u00e9"}}
	untrustedReport := fixture(t)
	untrustedReport.Outcome, untrustedReport.Failure = model.OutcomeError, &untrusted.Failure
	timeoutReport, timeoutErr := golden("timeout")
	cancelledReport, cancelledErr := golden("cancelled-partial")
	for _, c := range []struct {
		name   string
		runner runFunc
		code   int
		stderr string
	}{
		{"preflight missing tool", returning(orchestrate.FailureReport(missingQEMU), missingQEMU), 2,
			"targetcheck: error: missing_tool: QEMU executable is unavailable\n"},
		{"infrastructure timeout", returning(timeoutReport, timeoutErr), 2,
			"targetcheck: error: timeout: candidate security exceeded 5 seconds\n"},
		{"cancellation omits the repeated kind", returning(cancelledReport, cancelledErr), 130,
			"targetcheck: cancelled: caller cancelled during baseline compatibility\n"},
		{"untrusted message stays one escaped line", returning(untrustedReport, untrusted), 2,
			`targetcheck: error: io: baseline test "t\\n": cannot read command output: \x1b[2J\ncaf\u00e9` + "\n"},
		{"matched run prints no diagnostic", returning(fixture(t), nil), 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := temp(t)
			got := invoke(t, context.Background(), manifestFile(t, dir), dir, filepath.Join(dir, "report"), c.runner)
			if got.code != c.code || got.stderr != c.stderr {
				t.Fatalf("exit %d stderr %q, want %d %q", got.code, got.stderr, c.code, c.stderr)
			}
			if got.report == nil || !strings.HasPrefix(got.stdout, "targetcheck: "+string(got.report.Outcome)+"\nJSON: ") {
				t.Fatalf("missing report or stdout summary %q", got.stdout)
			}
			if c.stderr != "" && (got.report.Failure == nil || !strings.HasPrefix(c.stderr, "targetcheck: "+string(got.report.Outcome)+": ")) {
				t.Fatalf("diagnostic disagrees with report %s %+v", got.report.Outcome, got.report.Failure)
			}
		})
	}

	for _, c := range []struct{ name, manifest, message string }{
		{"duplicate key", `{"schema_version":1,"schema_version":1}`, `load manifest: invalid JSON manifest: duplicate key "schema_version"`},
		{"syntax error position", `{"schema_version":!}`, `load manifest: invalid JSON manifest at byte 19: invalid character '!' looking for beginning of value`},
	} {
		t.Run("malformed manifest keeps the loader's message once/"+c.name, func(t *testing.T) {
			dir := temp(t)
			root := filepath.Join(dir, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(dir, "bad.json")
			write(t, manifest, []byte(c.manifest))
			message := c.message
			got := invoke(t, context.Background(), manifest, root, filepath.Join(dir, "report"), nil)
			if got.code != 2 || got.stderr != "targetcheck: error: invalid_input: "+message+"\n" || got.report == nil || got.report.Failure.Message != message {
				t.Fatalf("exit %d stderr %q report %+v", got.code, got.stderr, got.report)
			}
			// Without any report, the diagnostic still names the run's failure first.
			got = invoke(t, context.Background(), manifest, root, filepath.Join(root, "report"), nil)
			want := "targetcheck: error: invalid_input: " + message + "; no report written: failure reports for an invalid manifest must be outside the input workspace\n"
			if got.code != 2 || got.stderr != want || got.report != nil || got.stdout != "" {
				t.Fatalf("exit %d stdout %q stderr %q, want %q", got.code, got.stdout, got.stderr, want)
			}
		})
	}

	t.Run("refused destination", func(t *testing.T) {
		dir := temp(t)
		existing := filepath.Join(dir, "existing")
		if err := os.Mkdir(existing, 0700); err != nil {
			t.Fatal(err)
		}
		got := invoke(t, context.Background(), manifestFile(t, dir), dir, existing, nil)
		if got.code != 2 || got.stderr != "targetcheck: error: invalid_input: output directory already exists\n" || got.stdout != "" {
			t.Fatalf("exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got = invoke(t, ctx, manifestFile(t, dir), dir, existing, nil)
		if got.code != 130 || got.stderr != "targetcheck: cancelled: run cancelled by caller; no report written: output directory already exists\n" || got.stdout != "" {
			t.Fatalf("cancelled: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
	})

	t.Run("failed reservation after a preflight failure", func(t *testing.T) {
		dir := temp(t)
		output := filepath.Join(dir, "report")
		runner := func(context.Context, model.Spec, model.Options, func() error) (model.Report, error) {
			if err := os.Mkdir(output, 0700); err != nil {
				t.Fatal(err)
			}
			return orchestrate.FailureReport(missingQEMU), missingQEMU
		}
		got := invoke(t, context.Background(), manifestFile(t, dir), dir, output, runner)
		want := "targetcheck: error: missing_tool: QEMU executable is unavailable; no report written: create new output: mkdir " + output + ": file exists\n"
		if got.code != 2 || got.stderr != want || got.stdout != "" {
			t.Fatalf("exit %d stdout %q stderr %q, want %q", got.code, got.stdout, got.stderr, want)
		}
	})

	t.Run("failed reservation inside the run", func(t *testing.T) {
		dir := temp(t)
		output := filepath.Join(dir, "report")
		runner := func(ctx context.Context, spec model.Spec, options model.Options, reserve func() error) (model.Report, error) {
			if err := os.Mkdir(output, 0700); err != nil {
				t.Fatal(err)
			}
			return (orchestrate.Engine{Planner: reservingPlanner{}, Executor: refusingExecutor{t}}).Run(ctx, spec, options, reserve)
		}
		got := invoke(t, context.Background(), manifestFile(t, dir), dir, output, runner)
		want := "targetcheck: error: io: create new output: mkdir " + output + ": file exists\n"
		if got.code != 2 || got.stderr != want || got.stdout != "" {
			t.Fatalf("exit %d stdout %q stderr %q, want %q", got.code, got.stdout, got.stderr, want)
		}
	})

	t.Run("persistence failure names the incomplete output", func(t *testing.T) {
		dir := temp(t)
		output := filepath.Join(dir, "report")
		runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
			if err := reserve(); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(output, "report.md"), 0700); err != nil {
				t.Fatal(err)
			}
			return timeoutReport, timeoutErr
		}
		got := invoke(t, context.Background(), manifestFile(t, dir), dir, output, runner)
		prefix, suffix := "targetcheck: error: io: output persistence failed: ", "; incomplete output at "+strconv.QuoteToASCII(output)+"\n"
		if got.code != 2 || !strings.HasPrefix(got.stderr, prefix) || !strings.HasSuffix(got.stderr, suffix) || strings.Count(got.stderr, "\n") != 1 || got.stdout != "" {
			t.Fatalf("exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		if got.report == nil || got.report.Failure == nil || "targetcheck: error: io: "+got.report.Failure.Message+suffix != got.stderr {
			t.Fatalf("diagnostic disagrees with the incomplete report: %q %+v", got.stderr, got.report)
		}
	})
}

// reservingPlanner accepts the fixture manifest without inspecting a workspace.
type reservingPlanner struct{ plan.Service }

func (reservingPlanner) Prepare(s model.Spec, _ model.Options) (model.Plan, error) {
	return model.Plan{Spec: s, Inputs: []model.FileIdentity{}, Tools: []model.ToolIdentity{}}, nil
}

type refusingExecutor struct{ t *testing.T }

func (e refusingExecutor) Execute(context.Context, model.CommandRequest) model.ProcessObservation {
	e.t.Fatal("a command ran after the destination reservation failed")
	return model.ProcessObservation{}
}

func TestCancellationAfterExecutionRevisesReports(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	output := filepath.Join(dir, "report")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
		if err := reserve(); err != nil {
			t.Fatal(err)
		}
		cancel()
		return fixture(t), nil
	}
	if code := run(ctx, args(manifest, dir, output), io.Discard, io.Discard, runner); code != 130 {
		t.Fatalf("late cancellation returned %d", code)
	}
	r := loadReport(t, filepath.Join(output, "report.json"))
	if r.Outcome != model.OutcomeCancelled || r.Failure == nil || r.Failure.Kind != model.FailureCancelled {
		t.Fatalf("late cancellation lost: %+v", r)
	}
	md, err := os.ReadFile(filepath.Join(output, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(md, []byte("cancelled")) {
		t.Fatalf("Markdown lost late cancellation: %s", md)
	}
}

func TestLateCancellationRewriteFailureKeepsMarker(t *testing.T) {
	dir := temp(t)
	path := filepath.Join(dir, "report")
	o, err := reserveOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	r := fixture(t)
	if err = persist(o, &r); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(path, "report.md")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(path, "report.md"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !markCancelled(ctx, &r) {
		t.Fatal("late cancellation not marked")
	}
	if err = o.cancelledReports(r); err == nil {
		t.Fatal("rewrite failure swallowed")
	}
	actual := loadReport(t, filepath.Join(path, "report.json"))
	if actual.Outcome != model.OutcomeCancelled {
		t.Fatalf("rewrite lost cancellation: %+v", actual)
	}
	if _, err = os.Stat(filepath.Join(path, incomplete)); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledReservationAndValidationRetainPriority(t *testing.T) {
	dir := temp(t)
	manifest := manifestFile(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	existing := filepath.Join(dir, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	if code := run(ctx, args(manifest, dir, existing), io.Discard, io.Discard, nil); code != 130 {
		t.Fatalf("cancelled validation code %d", code)
	}
	output := filepath.Join(dir, "report")
	runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		err := reserve()
		if err == nil {
			t.Fatal("collision accepted")
		}
		return orchestrate.FailureReport(err), err
	}
	if code := run(ctx, args(manifest, dir, output), io.Discard, io.Discard, runner); code != 130 {
		t.Fatalf("cancelled reserve code %d", code)
	}
}

func TestCancellationOutranksConsoleFailure(t *testing.T) {
	for _, broken := range []string{"stdout", "stderr"} {
		t.Run(broken, func(t *testing.T) {
			dir := temp(t)
			manifest := manifestFile(t, dir)
			output := filepath.Join(dir, "report")
			runner := func(_ context.Context, _ model.Spec, _ model.Options, reserve func() error) (model.Report, error) {
				if err := reserve(); err != nil {
					t.Fatal(err)
				}
				r := loadReport(t, "../render/testdata/cancelled-partial.json")
				return r, &model.RunError{Kind: model.ErrorCancelled, Failure: *r.Failure, Cause: context.Canceled}
			}
			var stdout, stderr io.Writer = io.Discard, io.Discard
			if broken == "stdout" {
				stdout = badWriter{}
			} else {
				stderr = badWriter{}
			}
			if code := run(context.Background(), args(manifest, dir, output), stdout, stderr, runner); code != 130 {
				t.Fatalf("cancelled console error returned %d", code)
			}
		})
	}
}
