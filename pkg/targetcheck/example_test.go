// SPDX-License-Identifier: Apache-2.0

package targetcheck_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/firmdura/targetcheck/pkg/targetcheck"
)

func ExampleLoad() {
	spec, err := targetcheck.Load(strings.NewReader(`{"schema_version":1,"target":{"kind":"native"},"variants":{"baseline":{"cwd":"baseline","build":[],"inputs":["source.c"],"artifacts":["runner"]},"candidate":{"cwd":"candidate","build":[],"inputs":["source.c"],"artifacts":["runner"]}},"test_inputs":[],"tests":[{"id":"regression","argv":["runner"],"timeout_seconds":5,"baseline_expect":{"exit_code":7,"stdout_contains":"known defect"},"candidate_expect":{"exit_code":0}}]}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(spec.Tests[0].ID)
	// Output: regression
}

func ExampleRun() {
	// A real caller loads a manifest and supplies its absolute workspace root.
	// A typed error also returns a partial report with the same failure cause.
	report, err := targetcheck.Run(context.Background(), targetcheck.Spec{}, targetcheck.Options{Root: "relative"})
	var runErr *targetcheck.RunError
	if errors.As(err, &runErr) {
		fmt.Println(runErr.Kind, report.Outcome)
	}
	// Output: invalid_input error
}
