// SPDX-License-Identifier: Apache-2.0

// Package render writes authoritative report data without running commands,
// inspecting files, or evaluating test expectations.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/firmdura/targetcheck/internal/model"
)

// Renderer implements the shared writer-only interface.
type Renderer struct{}

func (Renderer) WriteJSON(w io.Writer, r model.Report) error     { return WriteJSON(w, r) }
func (Renderer) WriteMarkdown(w io.Writer, r model.Report) error { return WriteMarkdown(w, r) }

var _ model.Renderer = Renderer{}

// WriteJSON writes indented schema-v1 JSON with a trailing newline. Raw captures
// are excluded by the shared model; no parallel wire model is introduced here.
func WriteJSON(w io.Writer, report model.Report) error {
	r, err := prepare(report)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return write(w, append(data, '\n'))
}

// WriteMarkdown writes the same states and evidence as JSON. Dynamic text uses
// quoted string notation; punctuation is encoded as text, never Markdown syntax.
func WriteMarkdown(w io.Writer, report model.Report) error {
	r, err := prepare(report)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# TargetCheck report\n\n- Schema version: %d\n- Outcome: %s\n", r.SchemaVersion, text(string(r.Outcome)))
	fmt.Fprintf(&b, "- Tool: %s; commit: %s; Go: %s\n", text(r.Tool.Version), text(r.Tool.Commit), text(r.Tool.GoVersion))
	fmt.Fprintf(&b, "- Target: %s; host OS: %s; host architecture: %s\n", text(string(r.Target.Kind)), text(r.Target.HostOS), text(r.Target.HostArch))
	if r.Target.Executable != "" {
		fmt.Fprintf(&b, "- QEMU executable: %s\n", text(r.Target.Executable))
	}
	if r.Target.Sysroot != "" {
		fmt.Fprintf(&b, "- Sysroot: %s\n", text(r.Target.Sysroot))
	}
	if r.Manifest != nil {
		fmt.Fprintf(&b, "- Manifest SHA-256: %s; encoding: %s\n", text(r.Manifest.SHA256), text(r.Manifest.Encoding))
	} else {
		b.WriteString("- Manifest identity: unavailable\n")
	}
	failure(&b, r.Failure)
	b.WriteString("\n## Declared coverage\n\n")
	if len(r.Declared) == 0 {
		b.WriteString("No declared execution coverage.\n")
	}
	for _, d := range r.Declared {
		fmt.Fprintf(&b, "- Variant: %s; build IDs: %s; test IDs: %s\n", text(string(d.Name)), jsonText(d.BuildIDs), jsonText(d.TestIDs))
	}
	b.WriteString("\n## Declared input identities\n\n")
	if len(r.Inputs) == 0 {
		b.WriteString("No input identities available.\n")
	}
	for _, f := range r.Inputs {
		file(&b, f)
	}
	b.WriteString("\n## Tool observations\n\n")
	if len(r.Tools) == 0 {
		b.WriteString("No tool observations available.\n")
	}
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "- Role: %s; path: %s", text(t.Role), text(t.Path))
		if t.Version != "" {
			fmt.Fprintf(&b, "; version: %s", text(t.Version))
		}
		if t.VersionError != "" {
			fmt.Fprintf(&b, "; version error: %s", text(t.VersionError))
		}
		b.WriteByte('\n')
	}
	if len(r.Variants) == 0 {
		b.WriteString("\nNo variant observations.\n")
	}
	for _, v := range r.Variants {
		fmt.Fprintf(&b, "\n## Variant %s\n\n- Cwd: %s\n", text(string(v.Name)), text(v.CWD))
		artifacts(&b, "Artifact identities before testing", v.ArtifactsBefore)
		checks(&b, "Build checks", v.Build)
		checks(&b, "Test checks", v.Tests)
		artifacts(&b, "Artifact identities after testing", v.ArtifactsAfter)
	}
	b.WriteString("\nIdentities cover declared files only. Outcomes describe the supplied expectations. Raw command logs can contain sensitive data.\n")
	return write(w, []byte(b.String()))
}

func checks(b *strings.Builder, title string, values []model.Check) {
	fmt.Fprintf(b, "\n### %s\n\n", title)
	if len(values) == 0 {
		b.WriteString("No checks declared in this list.\n")
	}
	for _, c := range values {
		fmt.Fprintf(b, "#### Check %d: %s\n\n", c.Index, text(c.ID))
		fmt.Fprintf(b, "- Kind: %s; status: %s\n- Reason: %s\n", text(string(c.Kind)), text(string(c.Status)), text(c.Reason))
		fmt.Fprintf(b, "- Planned argv: %s\n- Cwd: %s; timeout seconds: %d\n- Environment override names: %s\n", jsonText(c.Argv), text(c.CWD), c.TimeoutSeconds, jsonText(c.EnvNames))
		fmt.Fprintf(b, "- Expected normal exit: %d; stdout marker: %s; stderr marker: %s\n", c.Expectation.ExitCode, text(c.Expectation.StdoutContains), text(c.Expectation.StderrContains))
		if c.Observation == nil {
			b.WriteString("- Observation: unavailable\n\n")
			continue
		}
		o := c.Observation
		fmt.Fprintf(b, "- Started: %t; duration milliseconds: %d\n- Actual argv: %s\n- Actual cwd: %s; environment override names: %s\n", o.Started, o.DurationMillis, jsonText(o.Argv), text(o.CWD), jsonText(o.EnvNames))
		if o.ExitCode == nil {
			b.WriteString("- Actual normal exit: not observed\n")
		} else {
			fmt.Fprintf(b, "- Actual normal exit: %d\n", *o.ExitCode)
		}
		if o.Signal != "" {
			fmt.Fprintf(b, "- Signal: %s\n", text(o.Signal))
		}
		failure(b, o.Failure)
		capture(b, "stdout", o.Stdout)
		capture(b, "stderr", o.Stderr)
		b.WriteByte('\n')
	}
}
func capture(b *strings.Builder, stream string, c model.Capture) {
	fmt.Fprintf(b, "- %s: observed bytes: %d; truncated: %t; log: ", stream, c.Bytes, c.Truncated)
	if c.Log == "" {
		b.WriteString("unavailable\n")
	} else {
		fmt.Fprintf(b, "[%s log](%s)\n", stream, c.Log)
	}
}
func artifacts(b *strings.Builder, title string, values []model.ArtifactIdentity) {
	fmt.Fprintf(b, "\n### %s\n\n", title)
	if len(values) == 0 {
		b.WriteString("No artifact identities available.\n")
	}
	for _, a := range values {
		file(b, a.FileIdentity)
		fmt.Fprintf(b, "  ELF class: %s; machine: %s; endian: %s; ABI: %s\n", text(a.ELF.Class), text(a.ELF.Machine), text(a.ELF.Endian), text(a.ELF.ABI))
	}
}
func file(b *strings.Builder, f model.FileIdentity) {
	fmt.Fprintf(b, "- Path: %s; SHA-256: %s; size bytes: %d\n", text(f.Path), text(f.SHA256), f.SizeBytes)
}
func failure(b *strings.Builder, f *model.Failure) {
	if f != nil {
		fmt.Fprintf(b, "- Failure: %s; detail: %s\n", text(string(f.Kind)), text(f.Message))
	}
}
func text(s string) string      { return escape(strconv.QuoteToASCII(s)) }
func jsonText(value any) string { data, _ := json.Marshal(value); return escape(string(data)) }
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '!' && r <= '/' || r >= ':' && r <= '@' || r >= '[' && r <= '`' || r >= '{' && r <= '~' {
			fmt.Fprintf(&b, "&#%d;", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func write(w io.Writer, data []byte) error {
	if w == nil {
		return fmt.Errorf("report writer is required")
	}
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

var safeLog = regexp.MustCompile(`^logs/[A-Za-z0-9_-]+\.(stdout|stderr)$`)

// prepare validates representation boundaries, then sorts copies. It does not
// evaluate a marker, assign a status, inspect an identity or infer an outcome.
func prepare(r model.Report) (model.Report, error) {
	if r.SchemaVersion != model.SchemaVersion {
		return r, fmt.Errorf("unsupported report schema version %d", r.SchemaVersion)
	}
	if !validStrings(reflect.ValueOf(r)) {
		return r, fmt.Errorf("report contains invalid UTF-8")
	}
	if err := validateRepresentation(r); err != nil {
		return r, err
	}
	switch r.Outcome {
	case model.OutcomeMatched, model.OutcomeMismatch:
		if r.Failure != nil {
			return r, fmt.Errorf("behavioral report contains an aggregate failure")
		}
		if err := model.ValidateCoverage(r); err != nil {
			return r, fmt.Errorf("invalid report coverage: %w", err)
		}
	case model.OutcomeError, model.OutcomeCancelled:
		if r.Failure == nil {
			return r, fmt.Errorf("error or cancelled report requires its failure")
		}
	default:
		return r, fmt.Errorf("invalid report outcome %q", r.Outcome)
	}
	seen := map[model.VariantName]bool{}
	for _, v := range r.Variants {
		if v.Name != model.Baseline && v.Name != model.Candidate || seen[v.Name] {
			return r, fmt.Errorf("invalid or duplicate report variant %q", v.Name)
		}
		seen[v.Name] = true
		for _, list := range [][]model.Check{v.Build, v.Tests} {
			for _, c := range list {
				switch c.Status {
				case model.CheckMatched, model.CheckMismatch, model.CheckNotRun, model.CheckError:
				default:
					return r, fmt.Errorf("invalid check status %q", c.Status)
				}
				if r.Outcome == model.OutcomeMatched && c.Status != model.CheckMatched {
					return r, fmt.Errorf("matched report contains an incomplete or nonmatched check")
				}
				if r.Outcome == model.OutcomeMismatch && c.Status != model.CheckMatched && c.Status != model.CheckMismatch {
					return r, fmt.Errorf("mismatch report contains an infrastructure or unavailable check")
				}
				if c.Status == model.CheckNotRun && c.Observation != nil {
					return r, fmt.Errorf("not_run check contains an observation")
				}
				if c.Status == model.CheckMatched || c.Status == model.CheckMismatch {
					o := c.Observation
					if o == nil || !o.Started || o.ExitCode == nil || o.Failure != nil || o.Signal != "" {
						return r, fmt.Errorf("behavioral check lacks a normal process observation")
					}
				}
				if c.Observation != nil {
					for _, s := range []struct {
						name    string
						capture model.Capture
					}{{"stdout", c.Observation.Stdout}, {"stderr", c.Observation.Stderr}} {
						if s.capture.Bytes < 0 {
							return r, fmt.Errorf("negative capture byte count")
						}
						if s.capture.Log != "" && (!safeLog.MatchString(s.capture.Log) || !strings.HasSuffix(s.capture.Log, "."+s.name)) {
							return r, fmt.Errorf("unsafe %s log reference", s.name)
						}
					}
				}
			}
		}
	}
	// Allocate every sorted slice; never alter caller-owned backing arrays.
	r.Inputs = append([]model.FileIdentity{}, r.Inputs...)
	sort.SliceStable(r.Inputs, func(i, j int) bool { return r.Inputs[i].Path < r.Inputs[j].Path })
	r.Tools = append([]model.ToolIdentity{}, r.Tools...)
	sort.SliceStable(r.Tools, func(i, j int) bool {
		a, b := r.Tools[i], r.Tools[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Path < b.Path
	})
	r.Declared = append([]model.VariantCoverage{}, r.Declared...)
	for i := range r.Declared {
		r.Declared[i].BuildIDs = append([]string{}, r.Declared[i].BuildIDs...)
		r.Declared[i].TestIDs = append([]string{}, r.Declared[i].TestIDs...)
	}
	sort.SliceStable(r.Declared, func(i, j int) bool {
		return r.Declared[i].Name == model.Baseline && r.Declared[j].Name != model.Baseline
	})
	r.Variants = append([]model.VariantReport{}, r.Variants...)
	for i := range r.Variants {
		v := &r.Variants[i]
		v.Build = copyChecks(v.Build)
		v.Tests = copyChecks(v.Tests)
		v.ArtifactsBefore = copyArtifacts(v.ArtifactsBefore)
		v.ArtifactsAfter = copyArtifacts(v.ArtifactsAfter)
	}
	sort.SliceStable(r.Variants, func(i, j int) bool {
		return r.Variants[i].Name == model.Baseline && r.Variants[j].Name != model.Baseline
	})
	return r, nil
}
func copyChecks(values []model.Check) []model.Check {
	values = append([]model.Check{}, values...)
	for i := range values {
		c := &values[i]
		c.Argv = append([]string{}, c.Argv...)
		c.EnvNames = append([]string{}, c.EnvNames...)
		sort.Strings(c.EnvNames)
		if c.Observation != nil {
			o := *c.Observation
			o.Argv = append([]string{}, o.Argv...)
			o.EnvNames = append([]string{}, o.EnvNames...)
			sort.Strings(o.EnvNames)
			c.Observation = &o
		}
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].Index < values[j].Index })
	return values
}
func copyArtifacts(values []model.ArtifactIdentity) []model.ArtifactIdentity {
	values = append([]model.ArtifactIdentity{}, values...)
	sort.SliceStable(values, func(i, j int) bool { return values[i].Path < values[j].Path })
	return values
}
func validStrings(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer:
		if !v.IsNil() {
			return validStrings(v.Elem())
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).Tag.Get("json") != "-" && !validStrings(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if !validStrings(v.Index(i)) {
				return false
			}
		}
	}
	return true
}
