// SPDX-License-Identifier: Apache-2.0

package targetcheck

import (
	"github.com/firmdura/targetcheck/internal/plan"
	"io"
)

// Load strictly decodes one schema-v1 manifest, preserving its original bytes.
// It never starts commands. Run validates filesystem and target prerequisites.
func Load(r io.Reader) (Spec, error) { return plan.Load(r) }
