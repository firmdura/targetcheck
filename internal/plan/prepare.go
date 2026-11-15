// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
)

// Prepare validates both variants, inputs, tools and currently present artifacts
// before returning any runnable command requests. All returned maps/slices are
// independent of the caller's Spec; no global cwd or environment is changed.
func Prepare(spec model.Spec, options model.Options) (model.Plan, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return model.Plan{}, invalid("supported hosts are Linux amd64 and arm64", nil)
	}
	if err := validateSpec(spec); err != nil {
		return model.Plan{}, err
	}
	if !filepath.IsAbs(options.Root) || !validText(options.Root) {
		return model.Plan{}, invalid("Root must be an explicit absolute directory", nil)
	}
	root := filepath.Clean(options.Root)
	f, err := safeOpen(root, true)
	if err != nil {
		return model.Plan{}, invalid("invalid workspace root", err)
	}
	f.Close()
	canonicalSpec := spec
	if canonicalSpec.TestInputs == nil {
		canonicalSpec.TestInputs = []string{}
	}
	if canonicalSpec.Variants.Baseline.Build == nil {
		canonicalSpec.Variants.Baseline.Build = []model.Command{}
	}
	if canonicalSpec.Variants.Candidate.Build == nil {
		canonicalSpec.Variants.Candidate.Build = []model.Command{}
	}
	canonical, err := json.Marshal(canonicalSpec)
	if err != nil {
		return model.Plan{}, invalid("encode Spec identity", err)
	}
	if len(canonical) > model.MaxManifestBytes {
		return model.Plan{}, invalid("Spec JSON exceeds 1 MiB", nil)
	}
	var cloned model.Spec
	if err = json.Unmarshal(canonical, &cloned); err != nil {
		return model.Plan{}, invalid("copy Spec", err)
	}
	// Preserve the exact loaded bytes only when they still describe current fields.
	identityBytes, encoding := canonical, "canonical_json"
	if len(spec.ManifestJSON) > 0 && len(spec.ManifestJSON) <= model.MaxManifestBytes {
		loaded, e := Load(strings.NewReader(string(spec.ManifestJSON)))
		if e == nil {
			loaded.ManifestJSON = nil
			encoded, e := json.Marshal(loaded)
			if e == nil && string(encoded) == string(canonical) {
				identityBytes = spec.ManifestJSON
				encoding = "raw_json"
				cloned.ManifestJSON = append([]byte(nil), spec.ManifestJSON...)
			}
		}
	}
	sum := sha256.Sum256(identityBytes)
	p := model.Plan{Root: root, Spec: cloned, Manifest: model.ManifestIdentity{SHA256: hex.EncodeToString(sum[:]), Encoding: encoding}, Target: model.TargetInfo{Kind: spec.Target.Kind, HostOS: runtime.GOOS, HostArch: runtime.GOARCH}, Inputs: []model.FileIdentity{}, Tools: []model.ToolIdentity{}, Variants: []model.VariantPlan{}}
	if spec.Target.Kind == model.TargetQEMUARM {
		exe, e := resolveTool(spec.Target.Executable, root, nil)
		if e != nil {
			return model.Plan{}, missingTool("QEMU executable is unavailable", e)
		}
		sysroot := spec.Target.Sysroot
		if !filepath.IsAbs(sysroot) {
			sysroot = filepath.Join(root, sysroot)
		}
		sysroot, e = filepath.EvalSymlinks(sysroot)
		if e != nil {
			return model.Plan{}, invalid("QEMU sysroot is unavailable", bareError(e))
		}
		dir, e := os.Open(sysroot)
		if e != nil {
			return model.Plan{}, invalid("QEMU sysroot is unreadable", bareError(e))
		}
		info, e := dir.Stat()
		dir.Close()
		if e != nil || !info.IsDir() {
			return model.Plan{}, invalid("QEMU sysroot must be a directory", bareError(e))
		}
		// These stay absolute even inside Root: the executor passes exactly these
		// strings as the QEMU program and -L argument and records that argv
		// verbatim, and evaluation requires the observed prefix to equal Target.
		p.Target.Executable = exe
		p.Target.Sysroot = sysroot
		p.Tools = append(p.Tools, model.ToolIdentity{Role: "qemu", Path: exe, VersionError: "version not queried by process-free preflight"})
		p.Tools = append(p.Tools, model.ToolIdentity{Role: "sysroot", Path: sysroot, VersionError: "no universal sysroot version; directory environment is not attested"})
	}
	inputPaths := map[string]bool{}
	for i, v := range []model.Variant{spec.Variants.Baseline, spec.Variants.Candidate} {
		name := model.Baseline
		if i == 1 {
			name = model.Candidate
		}
		cwd, _ := relativePath(v.CWD)
		abs := filepath.Join(root, cwd)
		dir, e := safeOpen(abs, true)
		if e != nil {
			return model.Plan{}, invalid("variant directory "+cwd+" is unavailable", e)
		}
		dir.Close()
		vp := model.VariantPlan{Name: name, CWD: filepath.ToSlash(cwd), AbsoluteCWD: abs, Build: []model.CommandRequest{}, Tests: []model.CommandRequest{}, Artifacts: []string{}}
		for _, input := range v.Inputs {
			clean, _ := relativePath(input)
			inputPaths[filepath.ToSlash(filepath.Join(cwd, clean))] = true
		}
		for _, artifact := range v.Artifacts {
			clean, _ := relativePath(artifact)
			rel := filepath.Join(cwd, clean)
			exists, e := checkPath(root, rel, true)
			if e != nil {
				return model.Plan{}, invalid("unsafe artifact "+filepath.ToSlash(rel), e)
			}
			if !exists && len(v.Build) == 0 {
				return model.Plan{}, invalid("prebuilt artifact "+filepath.ToSlash(rel)+" is missing", nil)
			}
			if exists {
				if _, e = inspectArtifact(root, rel, p.Target); e != nil {
					return model.Plan{}, invalid("invalid existing artifact "+filepath.ToSlash(rel), bareError(e))
				}
			}
			vp.Artifacts = append(vp.Artifacts, filepath.ToSlash(clean))
		}
		for index, command := range v.Build {
			exe, e := resolveTool(command.Argv[0], abs, command.Env)
			if e != nil {
				return model.Plan{}, missingTool(fmt.Sprintf("%s build-%d executable is unavailable", name, index), e)
			}
			id := fmt.Sprintf("build-%d", index)
			// Launch the resolved absolute path; report its Root-relative form.
			vp.Build = append(vp.Build, model.CommandRequest{Variant: name, Kind: model.CheckBuild, ID: id, Target: p.Target, Argv: append([]string(nil), command.Argv...), ExecutablePath: exe, AbsoluteCWD: abs, RelativeCWD: vp.CWD, Env: cloneEnv(command.Env), TimeoutSeconds: command.TimeoutSeconds})
			p.Tools = append(p.Tools, model.ToolIdentity{Role: string(name) + "/" + id, Path: reportedPath(root, exe), VersionError: buildToolVersionError})
		}
		for _, test := range spec.Tests {
			args := append([]string(nil), test.Argv...)
			artifact, _ := relativePath(args[0])
			args[0] = "./" + filepath.ToSlash(artifact)
			vp.Tests = append(vp.Tests, model.CommandRequest{Variant: name, Kind: model.CheckTest, ID: test.ID, Target: p.Target, Argv: args, ExecutablePath: filepath.Join(abs, artifact), AbsoluteCWD: abs, RelativeCWD: vp.CWD, TimeoutSeconds: test.TimeoutSeconds})
		}
		p.Variants = append(p.Variants, vp)
	}
	if options.OutputDir != "" {
		if err = validateOutput(root, options.OutputDir, p.Variants); err != nil {
			return model.Plan{}, err
		}
	}
	for _, input := range spec.TestInputs {
		clean, _ := relativePath(input)
		inputPaths[filepath.ToSlash(clean)] = true
	}
	for rel := range inputPaths {
		identity, e := digestFile(root, rel)
		if e != nil {
			return model.Plan{}, invalid("declared input "+rel+" is unreadable or unsafe", e)
		}
		p.Inputs = append(p.Inputs, identity)
	}
	sort.Slice(p.Inputs, func(i, j int) bool { return p.Inputs[i].Path < p.Inputs[j].Path })
	sort.Slice(p.Tools, func(i, j int) bool {
		if p.Tools[i].Role != p.Tools[j].Role {
			return p.Tools[i].Role < p.Tools[j].Role
		}
		return p.Tools[i].Path < p.Tools[j].Path
	})
	return p, nil
}

// buildToolVersionError explains why build tools have no recorded version.
const buildToolVersionError = `not queried: build programs have no universal version option and are not run speculatively; record a version with an explicit build step such as ["gcc", "--version"], whose argv and captured output the report keeps`

func cloneEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func resolveTool(name, cwd string, env map[string]string) (string, error) {
	var candidates []string
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		candidates = []string{name}
	} else {
		path := os.Getenv("PATH")
		if value, exists := env["PATH"]; exists {
			path = value
		}
		for _, dir := range filepath.SplitList(path) {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return real, nil
		}
	}
	return "", fmt.Errorf("executable not found or not executable")
}

// reportedPath gives a resolved build tool its report form: relative to root
// with forward slashes when it lies inside root, like report cwd, input and
// artifact paths, and otherwise the absolute host path. Root has no symlink
// components (safeOpen) and resolved comes from EvalSymlinks, so this lexical
// containment test matches the location that is actually launched.
func reportedPath(root, resolved string) string {
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return resolved
	}
	return filepath.ToSlash(rel)
}

func validateOutput(root, output string, variants []model.VariantPlan) error {
	if !filepath.IsAbs(output) {
		return invalid("OutputDir must be absolute", nil)
	}
	output = filepath.Clean(output)
	for _, v := range variants {
		rel, err := filepath.Rel(v.AbsoluteCWD, output)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return invalid("output directory is inside variant workspace "+v.CWD, nil)
		}
	}
	if _, err := os.Lstat(output); err == nil {
		return invalid("output directory already exists", nil)
	} else if !os.IsNotExist(err) {
		return invalid("cannot inspect output directory", bareError(err))
	}
	// Inspect every existing ancestor, not just the final destination. Missing
	// intermediate output directories are rejected: the CLI creates only NEW_DIR.
	parent := filepath.Dir(output)
	dir, err := safeOpen(parent, true)
	if err != nil {
		return invalid("output parent is unavailable or symlinked", err)
	}
	dir.Close()
	return nil
}

func missingTool(message string, cause error) error {
	return &model.RunError{Kind: model.ErrorInvalidInput, Failure: model.Failure{Kind: model.FailureMissingTool, Message: message}, Cause: cause}
}
func bareError(err error) error {
	if err == nil {
		return nil
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
