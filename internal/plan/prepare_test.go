//go:build linux

// SPDX-License-Identifier: Apache-2.0

package plan_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/firmdura/targetcheck/internal/model"
	"github.com/firmdura/targetcheck/internal/plan"
)

func workspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"baseline", "candidate"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, name, "main.c"), []byte("source\n"), 0644)
	}
	write(t, filepath.Join(root, "cases.json"), []byte("shared cases\n"), 0644)
	return root
}
func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
func nativeArtifact(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareBothVariantsAndStableIdentities(t *testing.T) {
	root := workspace(t)
	s := baseSpec()
	s.Variants.Baseline.Build[0].Env = map[string]string{"PRIVATE_NAME": "secret value"}
	raw := encoded(t, s)
	loaded, err := plan.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Prepare(loaded, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Encoding != "raw_json" {
		t.Fatal("unchanged manifest lost exact identity")
	}
	hash := sha256.Sum256(raw)
	if p.Manifest.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("raw manifest digest differs")
	}
	if len(p.Variants) != 2 || p.Variants[0].Name != model.Baseline || p.Variants[1].Name != model.Candidate {
		t.Fatal("variant order differs")
	}
	if len(p.Inputs) != 3 {
		t.Fatalf("expected only declared files, got %+v", p.Inputs)
	}
	paths := []string{}
	for _, id := range p.Inputs {
		paths = append(paths, id.Path)
		data, err := os.ReadFile(filepath.Join(root, id.Path))
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256(data)
		if id.SHA256 != hex.EncodeToString(want[:]) || id.SizeBytes != int64(len(data)) {
			t.Fatalf("wrong declared identity %+v", id)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatal("digest order is unstable")
	}
	for _, v := range p.Variants {
		request := v.Tests[0]
		if request.Argv[0] != "./component" || request.Argv[1] != "" || request.RelativeCWD != v.CWD || request.ExecutablePath != filepath.Join(root, v.CWD, "component") {
			t.Fatalf("unsafe execution/report boundary %+v", request)
		}
	}
	second, err := plan.Prepare(loaded, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, second) {
		t.Fatal("same inputs produced different plan/identities")
	}
	loaded.Tests[0].Argv[1] = "caller mutation"
	loaded.Variants.Baseline.Build[0].Env["PRIVATE_NAME"] = "changed"
	loaded.ManifestJSON[0] = '['
	if p.Spec.Tests[0].Argv[1] != "" || p.Variants[0].Build[0].Env["PRIVATE_NAME"] != "secret value" || p.Spec.ManifestJSON[0] != '{' {
		t.Fatal("plan aliases caller mutable state")
	}
}

func TestManifestIdentityAfterMutationAndManualSpec(t *testing.T) {
	root := workspace(t)
	loaded, err := plan.Load(bytes.NewReader(encoded(t, baseSpec())))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Tests[0].Argv = append(loaded.Tests[0].Argv, "new argument")
	p, err := plan.Prepare(loaded, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Encoding != "canonical_json" {
		t.Fatal("stale original identity accepted")
	}
	canonical, err := json.Marshal(p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(canonical)
	if p.Manifest.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatal("wrong canonical Spec identity")
	}
	manual := baseSpec()
	manual.TestInputs = nil
	for _, name := range []string{"baseline", "candidate"} {
		nativeArtifact(t, filepath.Join(root, name, "component"))
	}
	manual.Variants.Baseline.Build = nil
	manual.Variants.Candidate.Build = nil
	p, err = plan.Prepare(manual, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = json.Marshal(p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Load(bytes.NewReader(canonical)); err != nil {
		t.Fatalf("manual Spec canonical identity is not a valid manifest: %v", err)
	}
}

func TestPreflightFailuresReturnNoRunnablePlan(t *testing.T) {
	cases := map[string]func(*testing.T, string, *model.Spec, *model.Options){
		"relative root": func(t *testing.T, root string, s *model.Spec, o *model.Options) { o.Root = "relative" },
		"missing input": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			s.Variants.Candidate.Inputs = []string{"missing"}
		},
		"unreadable input": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			if err := os.Chmod(filepath.Join(root, "candidate", "main.c"), 0); err != nil {
				t.Fatal(err)
			}
		},
		"directory input": func(t *testing.T, root string, s *model.Spec, o *model.Options) { s.TestInputs = []string{"baseline"} },
		"input symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			os.Remove(filepath.Join(root, "cases.json"))
			if err := os.Symlink("baseline/main.c", filepath.Join(root, "cases.json")); err != nil {
				t.Fatal(err)
			}
		},
		"ancestor symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			if err := os.Symlink("../baseline", filepath.Join(root, "candidate", "link")); err != nil {
				t.Fatal(err)
			}
			s.Variants.Candidate.Inputs = []string{"link/main.c"}
		},
		"variant symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			if err := os.Symlink("baseline", filepath.Join(root, "alias")); err != nil {
				t.Fatal(err)
			}
			s.Variants.Candidate.CWD = "alias"
		},
		"root symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(root, alias); err != nil {
				t.Fatal(err)
			}
			o.Root = alias
		},
		"missing candidate build tool": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			s.Variants.Candidate.Build = []model.Command{{Argv: []string{"/no/such/tool"}, TimeoutSeconds: 1}}
		},
		"nonexecutable tool": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			write(t, filepath.Join(root, "candidate", "tool"), []byte("data"), 0644)
			s.Variants.Candidate.Build = []model.Command{{Argv: []string{"./tool"}, TimeoutSeconds: 1}}
		},
		"missing prebuilt artifact": func(t *testing.T, root string, s *model.Spec, o *model.Options) { s.Variants.Baseline.Build = nil },
		"artifact symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			if err := os.Symlink("main.c", filepath.Join(root, "candidate", "component")); err != nil {
				t.Fatal(err)
			}
		},
		"artifact ancestor symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			os.Symlink("../baseline", filepath.Join(root, "candidate", "bin"))
			s.Variants.Candidate.Artifacts = []string{"bin/component"}
			s.Variants.Baseline.Artifacts = []string{"bin/component"}
			s.Tests[0].Argv[0] = "bin/component"
		},
		"wrong existing ELF": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			write(t, filepath.Join(root, "candidate", "component"), []byte("not ELF"), 0755)
		},
		"output inside candidate": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			o.OutputDir = filepath.Join(root, "candidate", "reports")
		},
		"output collision": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			o.OutputDir = filepath.Join(root, "cases.json")
		},
		"output parent symlink": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			os.Symlink(t.TempDir(), filepath.Join(root, "outputs"))
			o.OutputDir = filepath.Join(root, "outputs", "new")
		},
		"missing QEMU": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/missing/qemu-arm", Sysroot: root}
		},
		"missing sysroot": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/true", Sysroot: filepath.Join(root, "missing")}
		},
		"sysroot file": func(t *testing.T, root string, s *model.Spec, o *model.Options) {
			s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/true", Sysroot: filepath.Join(root, "cases.json")}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root := workspace(t)
			s := baseSpec()
			o := model.Options{Root: root}
			mutate(t, root, &s, &o)
			p, err := plan.Prepare(s, o)
			var typed *model.RunError
			if !errors.As(err, &typed) || typed.Kind != model.ErrorInvalidInput {
				t.Fatalf("expected input error, got %v", err)
			}
			if len(p.Variants) != 0 {
				t.Fatal("failed preflight exposed runnable requests")
			}
		})
	}
}

// Arbitrary build programs have no universal version option and are never run
// during preflight, so each build tool states that and how to record a version.
// The sysroot keeps its own reason; the QEMU version is queried after preflight.
func TestToolObservationsExplainMissingVersions(t *testing.T) {
	root := workspace(t)
	s := baseSpec()
	s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/true", Sysroot: root}
	p, err := plan.Prepare(s, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"baseline/build-0":  `not queried: build programs have no universal version option and are not run speculatively; record a version with an explicit build step such as ["gcc", "--version"], whose argv and captured output the report keeps`,
		"candidate/build-0": `not queried: build programs have no universal version option and are not run speculatively; record a version with an explicit build step such as ["gcc", "--version"], whose argv and captured output the report keeps`,
		"qemu":              "version not queried by process-free preflight",
		"sysroot":           "no universal sysroot version; directory environment is not attested",
	}
	if len(p.Tools) != len(want) {
		t.Fatalf("tools %+v", p.Tools)
	}
	for _, tool := range p.Tools {
		if tool.Version != "" || tool.VersionError != want[tool.Role] {
			t.Errorf("%s: version %q error %q, want error %q", tool.Role, tool.Version, tool.VersionError, want[tool.Role])
		}
	}
}

// Existing directories do not make overlapping workspaces acceptable, while
// siblings whose names share a prefix are separate workspaces.
func TestPrepareRejectsOverlappingVariantWorkspaces(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "ab", "a/nested"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, dir, "main.c"), []byte("source\n"), 0644)
	}
	write(t, filepath.Join(root, "cases.json"), []byte("shared cases\n"), 0644)
	s := baseSpec()
	s.Variants.Baseline.CWD, s.Variants.Candidate.CWD = "a", "ab"
	p, err := plan.Prepare(s, model.Options{Root: root})
	if err != nil || len(p.Variants) != 2 || p.Variants[0].CWD != "a" || p.Variants[1].CWD != "ab" {
		t.Fatalf("sibling workspaces a and ab: %v %+v", err, p.Variants)
	}
	for _, cwds := range [][2]string{{"a", "a/nested"}, {"a/nested", "a"}, {"a", "a"}} {
		s.Variants.Baseline.CWD, s.Variants.Candidate.CWD = cwds[0], cwds[1]
		p, err = plan.Prepare(s, model.Options{Root: root})
		want := fmt.Sprintf("baseline cwd %q and candidate cwd %q overlap; variant directories must differ and neither may contain the other", cwds[0], cwds[1])
		var typed *model.RunError
		if !errors.As(err, &typed) || typed.Failure.Kind != model.FailureInvalidInput || typed.Failure.Message != want || len(p.Variants) != 0 {
			t.Fatalf("%q and %q: got %v and %d variants, want %q", cwds[0], cwds[1], err, len(p.Variants), want)
		}
	}
}

func TestToolResolutionUsesChildEnvWithoutChangingCaller(t *testing.T) {
	root := workspace(t)
	s := baseSpec()
	for _, name := range []string{"baseline", "candidate"} {
		dir := filepath.Join(root, name, "tools")
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "builder"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	}
	for _, v := range []*model.Variant{&s.Variants.Baseline, &s.Variants.Candidate} {
		v.Build = []model.Command{{Argv: []string{"builder", "space and ; metacharacters"}, Env: map[string]string{"PATH": "tools", "SECRET": "private value"}, TimeoutSeconds: 1}}
	}
	beforeEnv, beforeCWD := os.Environ(), mustCWD(t)
	p, err := plan.Prepare(s, model.Options{Root: root, OutputDir: filepath.Join(root, "new-output")})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.Variants {
		if v.Build[0].ExecutablePath != filepath.Join(root, v.CWD, "tools", "builder") || v.Build[0].Argv[1] != "space and ; metacharacters" {
			t.Fatal("argv/PATH resolution differs")
		}
	}
	if !reflect.DeepEqual(beforeEnv, os.Environ()) || beforeCWD != mustCWD(t) {
		t.Fatal("caller environment or cwd changed")
	}
}
func mustCWD(t *testing.T) string {
	t.Helper()
	s, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestConcurrentPrepareHasNoSharedMutableState(t *testing.T) {
	roots := []string{workspace(t), workspace(t)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, root := range roots {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			p, err := plan.Prepare(baseSpec(), model.Options{Root: root})
			if err != nil {
				errs <- err
				return
			}
			if p.Root != root || !strings.HasPrefix(p.Variants[0].AbsoluteCWD, root+"/") {
				errs <- errors.New("wrong invocation root")
			}
		}(root)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPostBuildArtifactRevalidationAndHash(t *testing.T) {
	root := workspace(t)
	p, err := plan.Prepare(baseSpec(), model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	v := p.Variants[0]
	if _, err = plan.InspectArtifacts(p, v); err == nil {
		t.Fatal("missing post-build artifact accepted")
	}
	path := filepath.Join(root, "baseline", "component")
	nativeArtifact(t, path)
	first, err := plan.InspectArtifacts(p, v)
	if err != nil {
		t.Fatal(err)
	}
	second, err := plan.InspectArtifacts(p, v)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same artifact identities vary")
	}
	if len(first) != 1 || first[0].Path != "baseline/component" || first[0].ELF.Class != "ELF64" {
		t.Fatalf("wrong native identity %+v", first)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("identity change\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	changed, err := plan.InspectArtifacts(p, v)
	if err != nil {
		t.Fatal(err)
	}
	if changed[0].SHA256 == first[0].SHA256 {
		t.Fatal("changed artifact has unchanged identity")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("main.c", path); err != nil {
		t.Fatal(err)
	}
	if _, err = plan.InspectArtifacts(p, v); err == nil {
		t.Fatal("artifact replaced by symlink accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = plan.InspectArtifacts(p, v); err == nil {
		t.Fatal("artifact replaced by directory accepted")
	}
}

// Minimal headers are structural ELF fixtures only, never executed or presented
// as actual ARM acceptance evidence. Real ARM/QEMU execution belongs to #8.
func armELF(flags uint32) []byte {
	data := make([]byte, 52)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 40)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint32(data[24:], 0x10000)
	binary.LittleEndian.PutUint32(data[36:], flags)
	binary.LittleEndian.PutUint16(data[40:], 52)
	binary.LittleEndian.PutUint16(data[42:], 32)
	binary.LittleEndian.PutUint16(data[46:], 40)
	return data
}
func TestARMELFClassification(t *testing.T) {
	root := workspace(t)
	s := baseSpec()
	s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/true", Sysroot: root}
	p, err := plan.Prepare(s, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var foundSysroot bool
	for _, tool := range p.Tools {
		if tool.Role == "sysroot" {
			foundSysroot = true
			if tool.Path != root || tool.Version != "" || tool.VersionError == "" {
				t.Fatalf("unfaithful sysroot version observation %+v", tool)
			}
		}
	}
	if !foundSysroot {
		t.Fatal("sysroot version availability was not recorded")
	}
	v := p.Variants[0]
	path := filepath.Join(root, "baseline", "component")
	write(t, path, armELF(0x05000400), 0755)
	ids, err := plan.InspectArtifacts(p, v)
	if err != nil {
		t.Fatal(err)
	}
	if ids[0].ELF.ABI != "arm-eabi5-hard-float" || ids[0].ELF.Class != "ELF32" || ids[0].ELF.Machine != "EM_ARM" {
		t.Fatalf("wrong ARM classification %+v", ids)
	}
	for _, flags := range []uint32{0, 0x04000400, 0x05000200, 0x05000600} {
		write(t, path, armELF(flags), 0755)
		if _, err = plan.InspectArtifacts(p, v); err == nil {
			t.Fatalf("unsupported ARM flags %#x accepted", flags)
		}
	}
	data := armELF(0x05000400)
	binary.LittleEndian.PutUint16(data[18:], 3)
	write(t, path, data, 0755)
	if _, err = plan.InspectArtifacts(p, v); err == nil {
		t.Fatal("wrong ELF machine accepted")
	}
	write(t, path, armELF(0x05000400), 0755)
	native := p
	native.Target.Kind = model.TargetNative
	if _, err = plan.InspectArtifacts(native, v); err == nil {
		t.Fatal("ARM32 artifact accepted as native")
	}
}

func TestSubstitutedFIFOIsRejectedWithoutBlocking(t *testing.T) {
	root := workspace(t)
	path := filepath.Join(root, "cases.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := plan.Prepare(baseSpec(), model.Options{Root: root}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as input")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO preflight blocked")
	}
}

func TestARMDynamicLoaderMustBeInsideSuppliedSysroot(t *testing.T) {
	root := workspace(t)
	s := baseSpec()
	s.Target = model.Target{Kind: model.TargetQEMUARM, Executable: "/bin/true", Sysroot: root}
	p, err := plan.Prepare(s, model.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	header := armELF(0x05000400)
	interpreter := []byte("/lib/ld-linux-armhf.so.3\x00")
	binary.LittleEndian.PutUint32(header[28:], 52)
	binary.LittleEndian.PutUint16(header[44:], 1)
	ph := make([]byte, 32)
	binary.LittleEndian.PutUint32(ph, 3)
	binary.LittleEndian.PutUint32(ph[4:], 84)
	binary.LittleEndian.PutUint32(ph[16:], uint32(len(interpreter)))
	data := append(append(header, ph...), interpreter...)
	write(t, filepath.Join(root, "baseline", "component"), data, 0755)
	if _, err = plan.InspectArtifacts(p, p.Variants[0]); err == nil {
		t.Fatal("missing dynamic loader accepted")
	}
	if err = os.Mkdir(filepath.Join(root, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	loader := filepath.Join(root, "lib", "ld-linux-armhf.so.3")
	write(t, loader, []byte("structural loader-path fixture only"), 0755)
	if _, err = plan.InspectArtifacts(p, p.Variants[0]); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(loader); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/bin/true", loader); err != nil {
		t.Fatal(err)
	}
	if _, err = plan.InspectArtifacts(p, p.Variants[0]); err == nil {
		t.Fatal("dynamic loader escaping sysroot accepted")
	}
}
