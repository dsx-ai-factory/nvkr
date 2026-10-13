// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testreporting"
)

// ReportEvidence authenticates explicitly admitted observations and exports only
// local generated reports. TestGrid service publication is not implemented.
func (c *Client) ReportEvidence(ctx context.Context, inputs, format, out, gridTarget, gridOut string, options testobservation.RegistryOptions) (*testreporting.ExportResult, error) {
	if err := c.ready(ctx, "reporting test evidence"); err != nil {
		return nil, err
	}
	return testreporting.ExportFiles(ctx, inputs, format, out, gridTarget, gridOut, options)
}
