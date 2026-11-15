// SPDX-License-Identifier: Apache-2.0

package targetcheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLibraryDocumentationUsesCompiledExamples keeps docs/library.md in step
// with the package examples: every Go code block in the document must appear
// verbatim in an example_*_test.go file, where go test compiles and runs it,
// and a text block that directly follows a Go block, the printed output, must
// equal the // Output: comment of one of those examples. This checks
// documentation consistency only; the examples are the evidence.
func TestLibraryDocumentationUsesCompiledExamples(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "library.md"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("example_*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example files: %v", err)
	}
	var examples strings.Builder
	outputs := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		examples.Write(data)
		for _, output := range exampleOutputs(string(data)) {
			outputs[output] = true
		}
	}

	type block struct{ lang, text string }
	var blocks []block
	var lines []string
	lang, inside := "", false
	for _, line := range strings.Split(string(doc), "\n") {
		switch {
		case !inside && strings.HasPrefix(line, "```"):
			inside, lang, lines = true, strings.TrimPrefix(line, "```"), nil
		case inside && line == "```":
			inside = false
			blocks = append(blocks, block{lang, strings.Join(lines, "\n")})
		case inside:
			lines = append(lines, line)
		}
	}
	if inside {
		t.Fatal("docs/library.md has an unterminated code block")
	}
	code, printed := 0, 0
	for i, b := range blocks {
		switch {
		case b.lang == "go":
			code++
			if !strings.Contains(examples.String(), b.text) {
				t.Errorf("Go code block %d in docs/library.md is not verbatim in an example test:\n%s", code, b.text)
			}
		case b.lang == "text" && i > 0 && blocks[i-1].lang == "go":
			printed++
			if !outputs[b.text] {
				t.Errorf("the output shown after Go code block %d in docs/library.md is not an example's // Output: comment:\n%s", code, b.text)
			}
		}
	}
	if code == 0 || printed == 0 {
		t.Fatalf("docs/library.md has %d Go code blocks and %d output blocks; want at least one of each", code, printed)
	}
}

// exampleOutputs returns the text of each // Output: comment in Go source,
// one line per comment line without the comment marker, as go test compares it.
func exampleOutputs(source string) []string {
	var outputs, lines []string
	collecting := false
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "// Output:":
			collecting, lines = true, nil
		case collecting && strings.HasPrefix(trimmed, "//"):
			text := strings.TrimPrefix(strings.TrimLeft(line, " \t"), "//")
			lines = append(lines, strings.TrimPrefix(text, " "))
		case collecting:
			collecting = false
			outputs = append(outputs, strings.Join(lines, "\n"))
		}
	}
	return outputs
}
