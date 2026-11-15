//go:build linux

// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/orchestrate"
	"github.com/firmdura/targetcheck/internal/plan"
	"github.com/firmdura/targetcheck/internal/render"
)

const shellExit0 = "#!/bin/sh\nexit 0\n"

// realTempDir is symlink-free, as Prepare requires of Root and its ancestors.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// hostTool resolves a bare program through inherited PATH, as a manifest argv[0]
// without a slash is resolved, and returns its symlink-free location.
func hostTool(t *testing.T, name string) string {
	t.Helper()
	found, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("host program %s is required: %v", name, err)
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func toolPaths(tools []model.ToolIdentity) map[string]string {
	paths := map[string]string{}
	for _, tool := range tools {
		paths[tool.Role] = tool.Path
	}
	return paths
}

// toolWorkspace writes identical content below any root: per-variant source, a
// ./configure script and a tool reached through a relative PATH entry.
func toolWorkspace(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"baseline", "candidate"} {
		mkdirs(t, filepath.Join(root, name, "tools"))
		write(t, filepath.Join(root, name, "main.c"), []byte("source\n"), 0644)
		write(t, filepath.Join(root, name, "configure"), []byte(shellExit0), 0755)
		write(t, filepath.Join(root, name, "tools", "builder"), []byte(shellExit0), 0755)
	}
	write(t, filepath.Join(root, "cases.json"), []byte("shared cases\n"), 0644)
}

// withoutLaunchPaths clears only the execution-only absolute fields. Every value
// that orchestration copies into a report remains for comparison.
func withoutLaunchPaths(p model.Plan) model.Plan {
	strip := func(requests []model.CommandRequest) []model.CommandRequest {
		out := slices.Clone(requests)
		for i := range out {
			out[i].ExecutablePath, out[i].AbsoluteCWD = "", ""
		}
		return out
	}
	p.Root = ""
	p.Variants = slices.Clone(p.Variants)
	for i := range p.Variants {
		v := &p.Variants[i]
		v.AbsoluteCWD, v.Build, v.Tests = "", strip(v.Build), strip(v.Tests)
	}
	return p
}

func TestPlanToolPathsDoNotDependOnRootLocation(t *testing.T) {
	parent := realTempDir(t)
	sh := hostTool(t, "sh")
	s := baseSpec()
	for _, v := range []*model.Variant{&s.Variants.Baseline, &s.Variants.Candidate} {
		v.Build = []model.Command{
			{Argv: []string{"./configure", "--prefix=/usr"}, TimeoutSeconds: 60},
			{Argv: []string{"sh", "-c", "exit 0"}, TimeoutSeconds: 60},
			{Argv: []string{"builder"}, Env: map[string]string{"PATH": "tools"}, TimeoutSeconds: 60},
		}
	}
	raw := encoded(t, s)
	var reported []model.Plan
	for _, root := range []string{filepath.Join(parent, "a"), filepath.Join(parent, "nested", "much-longer-root")} {
		mkdirs(t, root)
		toolWorkspace(t, root)
		loaded, err := plan.Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.Prepare(loaded, model.Options{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		paths := toolPaths(p.Tools)
		for _, v := range p.Variants {
			// Workspace tools are Root-relative, even when found through PATH;
			// the inherited-PATH program keeps its absolute host location.
			want := []string{v.CWD + "/configure", sh, v.CWD + "/tools/builder"}
			launch := []string{filepath.Join(root, v.CWD, "configure"), sh, filepath.Join(root, v.CWD, "tools", "builder")}
			for i, request := range v.Build {
				if got := paths[fmt.Sprintf("%s/build-%d", v.Name, i)]; got != want[i] {
					t.Fatalf("%s build-%d tool path %q, want %q", v.Name, i, got, want[i])
				}
				// Launch behavior is unchanged: absolute resolved path and cwd.
				if request.ExecutablePath != launch[i] || request.AbsoluteCWD != filepath.Join(root, v.CWD) || request.Argv[0] != s.Variants.Baseline.Build[i].Argv[0] {
					t.Fatalf("launch request changed: %+v", request)
				}
			}
		}
		if len(paths) != 6 {
			t.Fatalf("unexpected tool observations %+v", p.Tools)
		}
		reported = append(reported, withoutLaunchPaths(p))
	}
	if !reflect.DeepEqual(reported[0], reported[1]) {
		t.Fatalf("report-visible plan depends on Root location:\n%+v\n%+v", reported[0], reported[1])
	}
}

func TestToolPathContainmentUsesResolvedLocation(t *testing.T) {
	parent := realTempDir(t)
	root := filepath.Join(parent, "root")
	sibling := root + "-sibling" // shares Root's string prefix, but lies outside it
	outside := filepath.Join(parent, "outside")
	mkdirs(t, root, sibling, outside)
	toolWorkspace(t, root)
	base := filepath.Join(root, "baseline")
	mkdirs(t, filepath.Join(base, "bin"), filepath.Join(base, "scripts"), filepath.Join(base, "..bin"), filepath.Join(root, "shared"))
	for _, path := range []string{filepath.Join(outside, "cc"), filepath.Join(base, "scripts", "gen"), filepath.Join(root, "shared", "cc"), filepath.Join(sibling, "tool"), filepath.Join(base, "..bin", "tool")} {
		write(t, path, []byte(shellExit0), 0755)
	}
	symlink(t, filepath.Join(outside, "cc"), filepath.Join(base, "bin", "cc"))             // inside -> outside
	symlink(t, filepath.Join(base, "scripts", "gen"), filepath.Join(outside, "gen"))       // outside -> inside
	symlink(t, filepath.Join("..", "shared", "cc"), filepath.Join(base, "cc-alias"))       // inside -> inside
	symlink(t, filepath.Join("..", "..", "outside", "cc"), filepath.Join(base, "rel-out")) // relative, leaves Root
	cc := filepath.Join(outside, "cc")
	cases := []struct{ argv0, path, report, launch string }{
		{"bin/cc", "", cc, cc},
		{filepath.Join(outside, "gen"), "", "baseline/scripts/gen", filepath.Join(base, "scripts", "gen")},
		{"./cc-alias", "", "shared/cc", filepath.Join(root, "shared", "cc")},
		{filepath.Join(sibling, "tool"), "", filepath.Join(sibling, "tool"), filepath.Join(sibling, "tool")},
		{"..bin/tool", "", "baseline/..bin/tool", filepath.Join(base, "..bin", "tool")},
		// Relative references that leave Root report absolute paths, which move
		// with Root because a different file is then launched.
		{"../../outside/cc", "", cc, cc},
		{"cc", "../../outside", cc, cc},
		{"./rel-out", "", cc, cc},
	}
	s := baseSpec()
	s.Variants.Baseline.Build = nil
	for _, c := range cases {
		command := model.Command{Argv: []string{c.argv0}, TimeoutSeconds: 1}
		if c.path != "" {
			command.Env = map[string]string{"PATH": c.path}
		}
		s.Variants.Baseline.Build = append(s.Variants.Baseline.Build, command)
	}
	p, err := plan.Prepare(s, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	paths := toolPaths(p.Tools)
	for i, c := range cases {
		request := p.Variants[0].Build[i]
		if got := paths[fmt.Sprintf("baseline/build-%d", i)]; got != c.report {
			t.Errorf("%s: reported %q, want %q", c.argv0, got, c.report)
		}
		if request.ExecutablePath != c.launch || request.Argv[0] != c.argv0 {
			t.Errorf("%s: launch %q argv0 %q, want %q", c.argv0, request.ExecutablePath, request.Argv[0], c.launch)
		}
	}
}

func TestQEMUPathsStayAbsoluteLaunchPaths(t *testing.T) {
	parent := realTempDir(t)
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	mkdirs(t, filepath.Join(root, "emulator"), filepath.Join(root, "sysroot"), filepath.Join(outside, "sysroot"))
	toolWorkspace(t, root)
	write(t, filepath.Join(root, "emulator", "qemu-arm"), []byte(shellExit0), 0755)
	symlink(t, filepath.Join(outside, "sysroot"), filepath.Join(root, "sysroot-link")) // inside -> outside
	symlink(t, filepath.Join(root, "sysroot"), filepath.Join(outside, "sysroot-link")) // outside -> inside
	hostTrue := hostTool(t, "true")
	cases := map[string]struct {
		target              model.Target
		executable, sysroot string
	}{
		"inside Root":              {model.Target{Executable: "emulator/qemu-arm", Sysroot: "sysroot"}, filepath.Join(root, "emulator", "qemu-arm"), filepath.Join(root, "sysroot")},
		"outside Root":             {model.Target{Executable: hostTrue, Sysroot: filepath.Join(outside, "sysroot")}, hostTrue, filepath.Join(outside, "sysroot")},
		"PATH executable":          {model.Target{Executable: "true", Sysroot: filepath.Join(outside, "sysroot")}, hostTrue, filepath.Join(outside, "sysroot")},
		"sysroot link leaves Root": {model.Target{Executable: hostTrue, Sysroot: "sysroot-link"}, hostTrue, filepath.Join(outside, "sysroot")},
		"sysroot link enters Root": {model.Target{Executable: hostTrue, Sysroot: filepath.Join(outside, "sysroot-link")}, hostTrue, filepath.Join(root, "sysroot")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			s.Target = c.target
			s.Target.Kind = model.TargetQEMUARM
			p, err := plan.Prepare(s, model.Options{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			paths := toolPaths(p.Tools)
			if p.Target.Executable != c.executable || p.Target.Sysroot != c.sysroot || paths["qemu"] != c.executable || paths["sysroot"] != c.sysroot {
				t.Fatalf("target %+v tools %+v, want resolved absolute %q and %q", p.Target, p.Tools, c.executable, c.sysroot)
			}
			// The executor wraps tests with request.Target; evaluation requires
			// that observed prefix to equal the reported target.
			for _, v := range p.Variants {
				for _, request := range append(slices.Clone(v.Build), v.Tests...) {
					if request.Target != p.Target {
						t.Fatalf("launch target %+v differs from reported target %+v", request.Target, p.Target)
					}
				}
			}
		})
	}
}

// A QEMU stand-in inside Root shows that the reported target remains bound to
// the observed QEMU argv. The ARM headers are structural fixtures: the stand-in
// never executes them, and this is not ARM/QEMU acceptance evidence.
func TestQEMUInsideRootKeepsReportedTargetBoundToObservedArgv(t *testing.T) {
	root := realTempDir(t)
	toolWorkspace(t, root)
	mkdirs(t, filepath.Join(root, "emulator"), filepath.Join(root, "sysroot"))
	write(t, filepath.Join(root, "emulator", "qemu-arm"), []byte(shellExit0), 0755)
	s := baseSpec()
	s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "emulator/qemu-arm", Sysroot: "sysroot"}
	s.Tests = []model.Test{{ID: "boundary", Argv: []string{"component"}, TimeoutSeconds: 10, BaselineExpect: model.Expectation{ExitCode: 0}, CandidateExpect: model.Expectation{ExitCode: 0}}}
	for _, v := range []*model.Variant{&s.Variants.Baseline, &s.Variants.Candidate} {
		v.Build = nil
		write(t, filepath.Join(root, v.CWD, "component"), armELF(0x05000400), 0755)
	}
	r, err := (orchestrate.Engine{}).Run(context.Background(), s, model.Options{Root: root}, nil)
	if err != nil || r.Outcome != model.OutcomeMatched {
		t.Fatalf("in-Root QEMU stand-in run: %v %+v", err, r.Failure)
	}
	want := []string{filepath.Join(root, "emulator", "qemu-arm"), "-L", filepath.Join(root, "sysroot"), "./component"}
	if r.Target.Executable != want[0] || r.Target.Sysroot != want[2] {
		t.Fatalf("reported target %+v", r.Target)
	}
	for _, v := range r.Variants {
		if got := v.Tests[0].Observation.Argv; !slices.Equal(got, want) {
			t.Fatalf("observed QEMU argv %q, want %q", got, want)
		}
	}
}

// Real builds and tests run under two roots. Durations are the only observations
// allowed to differ; with them cleared, JSON and Markdown must be byte-identical.
func TestReportsFromDifferentRootsDifferOnlyInDurations(t *testing.T) {
	parent := realTempDir(t)
	native, sh := hostTool(t, "true"), hostTool(t, "sh")
	elf, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	variant := func(cwd string) model.Variant {
		return model.Variant{CWD: cwd, Build: []model.Command{
			{Argv: []string{"./configure"}, TimeoutSeconds: 60},
			{Argv: []string{"sh", "-c", "test -x component"}, TimeoutSeconds: 60},
		}, Inputs: []string{"configure", "prebuilt"}, Artifacts: []string{"component"}}
	}
	s := model.Spec{SchemaVersion: 1, Target: model.Target{Kind: model.TargetNative}, Variants: model.Variants{Baseline: variant("baseline"), Candidate: variant("candidate")}, TestInputs: []string{"cases.json"},
		Tests: []model.Test{{ID: "same", Argv: []string{"component"}, TimeoutSeconds: 10, BaselineExpect: model.Expectation{ExitCode: 0}, CandidateExpect: model.Expectation{ExitCode: 0}}}}
	var rendered [][]byte
	for _, root := range []string{filepath.Join(parent, "a"), filepath.Join(parent, "nested", "much-longer-root")} {
		for _, name := range []string{"baseline", "candidate"} {
			mkdirs(t, filepath.Join(root, name))
			write(t, filepath.Join(root, name, "configure"), []byte("#!/bin/sh\nset -e\ncp prebuilt component\nchmod 755 component\n"), 0755)
			write(t, filepath.Join(root, name, "prebuilt"), elf, 0644)
		}
		write(t, filepath.Join(root, "cases.json"), []byte("shared cases\n"), 0644)
		r, err := (orchestrate.Engine{}).Run(context.Background(), s, model.Options{Root: root}, nil)
		if err != nil || r.Outcome != model.OutcomeMatched {
			t.Fatalf("run under %s: %v %+v", root, err, r.Failure)
		}
		paths := toolPaths(r.Tools)
		for _, name := range []string{"baseline", "candidate"} {
			if paths[name+"/build-0"] != name+"/configure" || paths[name+"/build-1"] != sh {
				t.Errorf("reported tools %+v", r.Tools)
			}
		}
		for i := range r.Variants {
			for _, checks := range [][]model.Check{r.Variants[i].Build, r.Variants[i].Tests} {
				for j := range checks {
					checks[j].Observation.DurationMillis = 0
				}
			}
		}
		var j, m bytes.Buffer
		if err := render.WriteJSON(&j, r); err != nil {
			t.Fatal(err)
		}
		if err := render.WriteMarkdown(&m, r); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(j.Bytes(), []byte(parent)) {
			t.Errorf("JSON under %s contains an absolute workspace path", root)
		}
		// Markdown quotes values and encodes punctuation such as "/" as character
		// references, so decode it before searching for the workspace location.
		if strings.Contains(html.UnescapeString(m.String()), parent) {
			t.Errorf("Markdown under %s contains an absolute workspace path", root)
		}
		rendered = append(rendered, j.Bytes(), m.Bytes())
	}
	if !bytes.Equal(rendered[0], rendered[2]) || !bytes.Equal(rendered[1], rendered[3]) {
		t.Fatalf("reports depend on Root location:\n%s\n%s", rendered[0], rendered[2])
	}
}
