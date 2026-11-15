//go:build !linux

// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"fmt"
	"os"
)

func safeOpen(string, bool) (*os.File, error) {
	return nil, fmt.Errorf("filesystem planning requires Linux")
}
