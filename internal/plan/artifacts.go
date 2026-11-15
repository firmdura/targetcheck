// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
)

// InspectArtifacts re-opens every declaration safely after builds/tests, hashes
// the same file descriptor used for ELF inspection, and returns sorted identities.
// It never accepts an empty identity for an unavailable artifact.
func InspectArtifacts(p model.Plan, v model.VariantPlan) ([]model.ArtifactIdentity, error) {
	fail := func(message string, err error) ([]model.ArtifactIdentity, error) {
		return nil, &model.RunError{Kind: model.ErrorInfrastructure, Failure: model.Failure{Kind: model.FailureArtifact, Message: message}, Cause: err}
	}
	if runtime.GOOS != "linux" || !filepath.IsAbs(p.Root) {
		return fail("artifact inspection requires an absolute Linux workspace", nil)
	}
	var declaration model.Variant
	switch v.Name {
	case model.Baseline:
		declaration = p.Spec.Variants.Baseline
	case model.Candidate:
		declaration = p.Spec.Variants.Candidate
	default:
		return fail("unknown artifact variant", nil)
	}
	cwd, err := relativePath(declaration.CWD)
	if err != nil || filepath.ToSlash(cwd) != v.CWD || filepath.Join(p.Root, cwd) != v.AbsoluteCWD {
		return fail("artifact variant disagrees with validated declaration", err)
	}
	if len(declaration.Artifacts) == 0 {
		return fail("artifact declarations are missing", nil)
	}
	identities := make([]model.ArtifactIdentity, 0, len(declaration.Artifacts))
	for _, name := range declaration.Artifacts {
		clean, err := relativePath(name)
		if err != nil {
			return fail("invalid artifact declaration", err)
		}
		rel := filepath.Join(cwd, clean)
		identity, err := inspectArtifact(p.Root, rel, p.Target)
		if err != nil {
			return fail("artifact "+filepath.ToSlash(rel)+": "+bareError(err).Error(), err)
		}
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].Path < identities[j].Path })
	return identities, nil
}

func inspectArtifact(root, relative string, target model.TargetInfo) (model.ArtifactIdentity, error) {
	if _, err := checkPath(root, relative, false); err != nil {
		return model.ArtifactIdentity{}, err
	}
	f, err := safeOpen(filepath.Join(root, relative), false)
	if err != nil {
		return model.ArtifactIdentity{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return model.ArtifactIdentity{}, bareError(err)
	}
	if info.Mode().Perm()&0111 == 0 {
		return model.ArtifactIdentity{}, fmt.Errorf("artifact is not executable")
	}
	elfFile, err := elf.NewFile(f)
	if err != nil {
		return model.ArtifactIdentity{}, fmt.Errorf("artifact is not a valid ELF: %w", err)
	}
	if elfFile.Data != elf.ELFDATA2LSB || (elfFile.Type != elf.ET_EXEC && elfFile.Type != elf.ET_DYN) || elfFile.Entry == 0 || (elfFile.OSABI != elf.ELFOSABI_NONE && elfFile.OSABI != elf.ELFOSABI_LINUX) {
		return model.ArtifactIdentity{}, fmt.Errorf("unsupported ELF encoding/type/entry")
	}
	abi := ""
	switch target.Kind {
	case model.TargetNative:
		expected := elf.EM_NONE
		if target.HostArch == "amd64" {
			expected = elf.EM_X86_64
			abi = "linux-amd64"
		} else if target.HostArch == "arm64" {
			expected = elf.EM_AARCH64
			abi = "linux-arm64"
		}
		if target.HostOS != "linux" || elfFile.Class != elf.ELFCLASS64 || elfFile.Machine != expected || expected == elf.EM_NONE {
			return model.ArtifactIdentity{}, fmt.Errorf("ELF does not match native Linux target")
		}
	case model.TargetQEMUARM:
		var flags [4]byte
		if _, err = f.ReadAt(flags[:], 36); err != nil {
			return model.ArtifactIdentity{}, fmt.Errorf("missing ARM ELF flags")
		}
		bits := binary.LittleEndian.Uint32(flags[:])
		if elfFile.Class != elf.ELFCLASS32 || elfFile.Machine != elf.EM_ARM || bits&0xff000000 != 0x05000000 || bits&0x400 == 0 || bits&0x200 != 0 {
			return model.ArtifactIdentity{}, fmt.Errorf("ELF is not ARM32 EABI5 hard-float")
		}
		abi = "arm-eabi5-hard-float"
	default:
		return model.ArtifactIdentity{}, fmt.Errorf("unsupported ELF target")
	}
	if target.Kind == model.TargetQEMUARM {
		for _, prog := range elfFile.Progs {
			if prog.Type != elf.PT_INTERP {
				continue
			}
			if prog.Filesz > 4096 {
				return model.ArtifactIdentity{}, fmt.Errorf("invalid ARM interpreter path")
			}
			data, err := io.ReadAll(io.LimitReader(prog.Open(), 4097))
			if err != nil {
				return model.ArtifactIdentity{}, err
			}
			if len(data) == 0 || data[len(data)-1] != 0 {
				return model.ArtifactIdentity{}, fmt.Errorf("unterminated ARM interpreter path")
			}
			path := strings.TrimSuffix(string(data), "\x00")
			if !strings.HasPrefix(path, "/") || strings.ContainsRune(path, 0) {
				return model.ArtifactIdentity{}, fmt.Errorf("invalid ARM interpreter path")
			}
			rel, err := relativePath(strings.TrimPrefix(path, "/"))
			if err != nil {
				return model.ArtifactIdentity{}, err
			}
			loader := filepath.Join(target.Sysroot, rel)
			real, err := filepath.EvalSymlinks(loader)
			if err != nil {
				return model.ArtifactIdentity{}, fmt.Errorf("ARM dynamic loader is missing from sysroot")
			}
			inside, err := filepath.Rel(target.Sysroot, real)
			if err != nil || inside == ".." || strings.HasPrefix(inside, "../") {
				return model.ArtifactIdentity{}, fmt.Errorf("ARM dynamic loader escapes sysroot")
			}
			info, err := os.Stat(real)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return model.ArtifactIdentity{}, fmt.Errorf("ARM dynamic loader is not executable")
			}
		}
	}
	identity, err := digestOpened(f, filepath.ToSlash(relative))
	if err != nil {
		return model.ArtifactIdentity{}, err
	}
	return model.ArtifactIdentity{FileIdentity: identity, ELF: model.ELFIdentity{Class: fmt.Sprintf("ELF%d", map[elf.Class]int{elf.ELFCLASS32: 32, elf.ELFCLASS64: 64}[elfFile.Class]), Machine: elfFile.Machine.String(), Endian: "little", ABI: abi}}, nil
}
