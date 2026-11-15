//go:build !linux

// SPDX-License-Identifier: Apache-2.0

package execute

import (
	"context"
	"github.com/firmdura/targetcheck/internal/model"
)

// Executor implements model.Executor. Process execution requires Linux.
type Executor struct{}

func (Executor) Execute(ctx context.Context, request model.CommandRequest) model.ProcessObservation {
	return Execute(ctx, request)
}

// Execute reports the unsupported host without launching a program.
func Execute(ctx context.Context, request model.CommandRequest) model.ProcessObservation {
	kind := model.FailureInvalidInput
	message := "command execution requires Linux"
	if ctx != nil && ctx.Err() != nil {
		kind, message = model.FailureCancelled, "command cancelled before launch"
	}
	return model.ProcessObservation{Argv: append([]string{}, request.Argv...), CWD: request.RelativeCWD, EnvNames: []string{}, Failure: &model.Failure{Kind: kind, Message: message}}
}
