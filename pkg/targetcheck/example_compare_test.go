//go:build linux

// SPDX-License-Identifier: Apache-2.0

package targetcheck_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/firmdura/targetcheck/pkg/targetcheck"
)

// portComponent is a tiny component. Built without -DFIXED it has a known defect:
// it accepts 70000 as a port number. Built with -DFIXED it rejects it.
const portComponent = `#include <stdio.h>
#include <stdlib.h>

static int parse_port(const char *text, long *port)
{
	char *end;
	long value = strtol(text, &end, 10);

	if (*text == '\0' || *end != '\0' || value < 0)
		return -1;
#ifdef FIXED
	if (value > 65535)
		return -1;
#endif
	*port = value;
	return 0;
}

int main(void)
{
	long port;

	if (parse_port("70000", &port) == 0) {
		printf("FAIL reject-out-of-range: accepted %ld\n", port);
		return 1;
	}
	puts("PASS reject-out-of-range");
	return 0;
}
`

// portManifest builds the baseline and the candidate with different trusted
// commands and runs one shared test. The baseline's known failure is an exact
// exit code plus a literal marker; the candidate must exit 0.
const portManifest = `{
  "schema_version": 1,
  "target": {"kind": "native"},
  "variants": {
    "baseline": {
      "cwd": "baseline",
      "build": [{"argv": ["cc", "-o", "port-check", "port.c"], "timeout_seconds": 120}],
      "inputs": ["port.c"],
      "artifacts": ["port-check"]
    },
    "candidate": {
      "cwd": "candidate",
      "build": [{"argv": ["cc", "-DFIXED", "-o", "port-check", "port.c"], "timeout_seconds": 120}],
      "inputs": ["port.c"],
      "artifacts": ["port-check"]
    }
  },
  "test_inputs": [],
  "tests": [{
    "id": "reject-out-of-range",
    "argv": ["port-check"],
    "timeout_seconds": 10,
    "baseline_expect": {"exit_code": 1, "stdout_contains": "FAIL reject-out-of-range: accepted 70000"},
    "candidate_expect": {"exit_code": 0, "stdout_contains": "PASS reject-out-of-range"}
  }]
}`

// Example_compareVariants builds and tests a baseline and a candidate natively,
// then writes the same evidence as JSON and Markdown. It needs Linux and cc.
func Example_compareVariants() {
	// Options.Root must be an absolute path without symlinks.
	root, err := os.MkdirTemp("", "targetcheck-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	if root, err = filepath.EvalSymlinks(root); err != nil {
		panic(err)
	}
	for _, variant := range []string{"baseline", "candidate"} {
		if err := os.Mkdir(filepath.Join(root, variant), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(root, variant, "port.c"), []byte(portComponent), 0o644); err != nil {
			panic(err)
		}
	}

	spec, err := targetcheck.Load(strings.NewReader(portManifest))
	if err != nil {
		panic(err) // a *targetcheck.RunError of kind invalid_input
	}
	report, err := targetcheck.Run(context.Background(), spec, targetcheck.Options{Root: root})
	var runErr *targetcheck.RunError
	if errors.As(err, &runErr) {
		// Invalid input, infrastructure failure or cancellation. The partial
		// report still records completed checks; the rest are not_run.
		fmt.Println("run failed:", runErr.Kind, runErr.Failure.Kind, report.Outcome)
		return
	}

	// A behavior mismatch is not a Go error: inspect the outcome and checks.
	// With a nil error, every check ran and exited normally.
	fmt.Println("outcome:", report.Outcome)
	for _, variant := range report.Variants {
		for _, test := range variant.Tests {
			fmt.Printf("%s %s: %s, exit %d\n", variant.Name, test.ID, test.Status, *test.Observation.ExitCode)
			fmt.Printf("  stdout: %q\n", test.Observation.Stdout.Data)
		}
	}

	// Run never writes files. Render the evidence wherever you need it.
	var jsonReport, markdownReport bytes.Buffer
	if err := targetcheck.WriteJSON(&jsonReport, report); err != nil {
		panic(err)
	}
	if err := targetcheck.WriteMarkdown(&markdownReport, report); err != nil {
		panic(err)
	}
	fmt.Println(strings.SplitN(markdownReport.String(), "\n", 2)[0])

	// The exit status the targetcheck CLI uses for this outcome.
	exitCode := 2 // error, and anything unexpected
	switch report.Outcome {
	case targetcheck.OutcomeMatched:
		exitCode = 0
	case targetcheck.OutcomeMismatch:
		exitCode = 1
	case targetcheck.OutcomeCancelled:
		exitCode = 130
	}
	fmt.Println("exit code:", exitCode)
	// Output:
	// outcome: matched
	// baseline reject-out-of-range: matched, exit 1
	//   stdout: "FAIL reject-out-of-range: accepted 70000\n"
	// candidate reject-out-of-range: matched, exit 0
	//   stdout: "PASS reject-out-of-range\n"
	// # TargetCheck report
	// exit code: 0
}
