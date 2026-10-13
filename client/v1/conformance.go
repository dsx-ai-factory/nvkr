// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/conformance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

func (c *Client) RunConformance(ctx context.Context, configuration string) (*conformance.Result, error) {
	if err := c.ready(ctx, "running Kubernetes conformance"); err != nil {
		return nil, err
	}
	return conformance.RunFiles(ctx, configuration)
}
func (c *Client) QualifyWithSuites(ctx context.Context, options qualification.FilesOptionsV2) (*qualification.PublicationV2, error) {
	if err := c.ready(ctx, "qualifying with authenticated suites"); err != nil {
		return nil, err
	}
	return qualification.PublishFilesV2(ctx, options)
}

// QualifyWithSuiteConfiguration admits the facade before the owning module
// opens the private suite policy and transport configuration.
func (c *Client) QualifyWithSuiteConfiguration(ctx context.Context, configuration string, options qualification.FilesOptionsV2) (*qualification.PublicationV2, error) {
	if err := c.ready(ctx, "qualifying with configured authenticated suites"); err != nil {
		return nil, err
	}
	return qualification.PublishConfiguredFilesV2(ctx, configuration, options)
}
