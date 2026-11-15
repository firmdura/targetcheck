//go:build linux

// SPDX-License-Identifier: Apache-2.0

package targetcheck_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/firmdura/targetcheck/pkg/targetcheck"
)

// slowManifest declares baseline and candidate builds that take longer than
// the caller is willing to wait.
const slowManifest = `{
  "schema_version": 1,
  "target": {"kind": "native"},
  "variants": {
    "baseline": {
      "cwd": "baseline",
      "build": [{"argv": ["sleep", "60"], "timeout_seconds": 120}],
      "inputs": ["input.txt"],
      "artifacts": ["program"]
    },
    "candidate": {
      "cwd": "candidate",
      "build": [{"argv": ["sleep", "60"], "timeout_seconds": 120}],
      "inputs": ["input.txt"],
      "artifacts": ["program"]
    }
  },
  "test_inputs": [],
  "tests": [{
    "id": "smoke",
    "argv": ["program"],
    "timeout_seconds": 10,
    "baseline_expect": {"exit_code": 0},
    "candidate_expect": {"exit_code": 0}
  }]
}`

// ExampleRun_deadline shows that a caller deadline, like any context
// cancellation, stops the running command and returns a cancelled partial
// report. Checks that did not complete are never reported as matched. It needs
// Linux and sleep.
func ExampleRun_deadline() {
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
		if err := os.WriteFile(filepath.Join(root, variant, "input.txt"), []byte("declared input\n"), 0o644); err != nil {
			panic(err)
		}
	}
	spec, err := targetcheck.Load(strings.NewReader(slowManifest))
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	report, err := targetcheck.Run(ctx, spec, targetcheck.Options{Root: root})

	var runErr *targetcheck.RunError
	if errors.As(err, &runErr) && runErr.Kind == targetcheck.ErrorCancelled {
		fmt.Println("cancelled:", runErr.Failure.Message)
	}
	fmt.Println("error:", err)
	fmt.Println("deadline exceeded:", errors.Is(err, context.DeadlineExceeded))
	fmt.Println("outcome:", report.Outcome)
	for _, variant := range report.Variants {
		for _, checks := range [][]targetcheck.Check{variant.Build, variant.Tests} {
			for _, check := range checks {
				fmt.Printf("%s %s %s: %s\n", variant.Name, check.Kind, check.ID, check.Status)
			}
		}
	}
	// Output:
	// cancelled: run cancelled by caller
	// error: cancelled: run cancelled by caller
	// deadline exceeded: true
	// outcome: cancelled
	// baseline build build-0: error
	// baseline test smoke: not_run
	// candidate build build-0: not_run
	// candidate test smoke: not_run
}
