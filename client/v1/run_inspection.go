// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

type RunInspection = lifecycle.RunInspection
type InspectedRunOutput = lifecycle.InspectedRunOutput

// InspectRun returns bounded, safe stored metadata, without payload verification
// or execution/restoration of the recorded attempt.
func (c *Client) InspectRun(ctx context.Context, recordPath string) (RunInspection, error) {
	if err := c.ready(ctx, "Run inspection"); err != nil {
		return RunInspection{}, err
	}
	return lifecycle.InspectRun(ctx, recordPath)
}
