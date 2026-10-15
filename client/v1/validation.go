// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validation"
)

type TestOutput = testresults.Output
type ValidationOptions = lifecycle.QualificationRuntimeOptions

// Validate delegates profile admission, source authentication and diagnostic
// execution. An empty name selects configured checks; outputs grant no authority.
func (c *Client) Validate(ctx context.Context, checkName, configPath string, options ValidationOptions) ([]TestOutput, error) {
	if err := c.ready(ctx, "diagnostic validation"); err != nil {
		return nil, err
	}
	return validation.RunFiles(ctx, checkName, configPath, options)
}
