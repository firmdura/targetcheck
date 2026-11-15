// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// go doc and pkg.go.dev present the comment directly above a package clause
// as package documentation. Every package of the module, including tests and
// build-constrained files, must keep its license header out of that comment,
// and each non-test package documents itself in exactly one file.
func TestPackageDocumentationExcludesLicenseHeaders(t *testing.T) {
	root := filepath.Join("..", "..")
	documented := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(filepath.Dir(path)) + " package " + file.Name.Name
		if _, seen := documented[key]; !seen {
			documented[key] = nil
		}
		if file.Doc == nil {
			return nil
		}
		if text := file.Doc.Text(); strings.Contains(text, "SPDX-License-Identifier") {
			t.Errorf("%s: package documentation contains the license header: %q", path, text)
		}
		documented[key] = append(documented[key], filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(documented))
	for key := range documented {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) < 10 {
		t.Fatalf("found only %d packages: %v", len(keys), keys)
	}
	for _, key := range keys {
		files := documented[key]
		if strings.HasSuffix(key, "_test") {
			if len(files) != 0 {
				t.Errorf("%s: external test package has package documentation in %v", key, files)
			}
		} else if len(files) != 1 {
			t.Errorf("%s: package documentation in %d files %v, want exactly one", key, len(files), files)
		}
	}
}
