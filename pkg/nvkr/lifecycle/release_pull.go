// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// PullOptions bounds a registry download of one named output. WorkspaceRoot
// is an optional parent for temporary storage; zero Timeout selects ten minutes.
type PullOptions struct {
	WorkspaceRoot string
	Registry      delivery.RegistryOptions
	Timeout       time.Duration
}

// PullReleaseOutput checks the release metadata and downloads one exact named
// output into a new directory. It checks payload integrity, but does
// not verify signatures or consumer policy and returns no acceptance capability.
func PullReleaseOutput(ctx context.Context, reference, output, out string, options PullOptions) (delivery.PulledOutput, error) {
	if ctx == nil {
		return delivery.PulledOutput{}, e.New(e.CodeUsage, "release pull requires a context")
	}
	if err := ctx.Err(); err != nil {
		return delivery.PulledOutput{}, e.Wrap(e.CodeTimeout, err, "release pull canceled")
	}
	if options.Timeout < 0 {
		return delivery.PulledOutput{}, e.New(e.CodeUsage, "release pull timeout must be positive")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := delivery.PullOutput(ctx, reference, output, out, options.WorkspaceRoot, options.Registry)
	if err != nil && ctx.Err() != nil {
		return delivery.PulledOutput{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release pull canceled")
	}
	return result, err
}
