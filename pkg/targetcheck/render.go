// SPDX-License-Identifier: Apache-2.0

package targetcheck

import (
	"github.com/firmdura/targetcheck/internal/render"
	"io"
)

// WriteJSON writes schema-v1 evidence to w, excluding raw capture bytes.
func WriteJSON(w io.Writer, r Report) error { return render.WriteJSON(w, r) }

// WriteMarkdown writes the same evidence with escaped text and safe log links.
func WriteMarkdown(w io.Writer, r Report) error { return render.WriteMarkdown(w, r) }
