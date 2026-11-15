#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
# These tests compile native C helpers and the actual CLI. No mocked process
# observations or network access satisfy this integration target.
${GO:-go} test -race -count=1 ./pkg/targetcheck/... ./internal/orchestrate/... ./internal/cli/... ./cmd/targetcheck/...
