// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/firmdura/targetcheck/internal/model"
)

func checkPath(root, relative string, allowAbsent bool) (bool, error) {
	clean, err := relativePath(relative)
	if err != nil {
		return false, err
	}
	path := root
	parts := strings.Split(clean, string(filepath.Separator))
	for index, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) && allowAbsent {
				return false, nil
			}
			return false, bareError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("symlink traversal is forbidden")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return false, fmt.Errorf("path ancestor is not a directory")
		}
	}
	return true, nil
}

func digestFile(root, relative string) (model.FileIdentity, error) {
	if _, err := checkPath(root, relative, false); err != nil {
		return model.FileIdentity{}, err
	}
	f, err := safeOpen(filepath.Join(root, relative), false)
	if err != nil {
		return model.FileIdentity{}, err
	}
	defer f.Close()
	return digestOpened(f, filepath.ToSlash(relative))
}

func digestOpened(f *os.File, relative string) (model.FileIdentity, error) {
	info, err := f.Stat()
	if err != nil {
		return model.FileIdentity{}, bareError(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0444 == 0 {
		return model.FileIdentity{}, fmt.Errorf("declared file must be ordinary and readable")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return model.FileIdentity{}, bareError(err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return model.FileIdentity{}, bareError(err)
	}
	after, err := f.Stat()
	if err != nil {
		return model.FileIdentity{}, bareError(err)
	}
	if n != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return model.FileIdentity{}, fmt.Errorf("declared file changed while hashing")
	}
	return model.FileIdentity{Path: relative, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: n}, nil
}
