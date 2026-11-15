// SPDX-License-Identifier: Apache-2.0

package targetcheck

import (
	"context"
	"github.com/firmdura/targetcheck/internal/orchestrate"
)

// Run preflights both variants, then builds/tests baseline followed by candidate.
// Ordinary expectation mismatches return a report without an error. Invalid
// input, infrastructure failures and cancellation return a partial report and
// *RunError. Run never writes reports or changes the caller cwd/environment.
// Configured build/test commands are trusted arbitrary programs, not sandboxed.
func Run(ctx context.Context, spec Spec, options Options) (Report, error) {
	return (orchestrate.Engine{}).Run(ctx, spec, options, nil)
}
