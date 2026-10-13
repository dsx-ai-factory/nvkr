// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/conformance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

func (c *Client) RunConformance(ctx context.Context, configuration string) (*conformance.Result, error) {
	if err := c.ready(ctx, "running Kubernetes conformance"); err != nil {
		return nil, err
	}
	return conformance.RunFiles(ctx, configuration)
}

// QualifyTriggeredWithSuites delegates event source authentication, mandatory
// native smoke and passive suite admission to their single qualification owner.
func (c *Client) QualifyTriggeredWithSuites(ctx context.Context, policy, configuration string, options lifecycle.QualificationRuntimeOptions) (*qualification.PublicationV3, error) {
	if err := c.ready(ctx, "qualifying the triggering Run with authenticated suites"); err != nil {
		return nil, err
	}
	return qualification.PublishTriggeredRunV3(ctx, policy, configuration, options)
}
