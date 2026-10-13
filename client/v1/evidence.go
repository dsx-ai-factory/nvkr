// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func (c *Client) EmitEvidence(ctx context.Context, root, description, selection, out string) (string, error) {
	if err := c.ready(ctx, "emitting test evidence"); err != nil {
		return "", err
	}
	return testobservation.EmitFiles(ctx, root, description, selection, out)
}
func (c *Client) PublishEvidence(ctx context.Context, root, repository string, options testobservation.RegistryOptions) (*testobservation.Published, error) {
	if err := c.ready(ctx, "publishing test evidence"); err != nil {
		return nil, err
	}
	return testobservation.Publish(ctx, root, repository, options)
}
func (c *Client) VerifyEvidence(ctx context.Context, reference string, expected testobservation.Expected, options testobservation.RegistryOptions) (*testobservation.Verified, error) {
	if err := c.ready(ctx, "verifying test evidence"); err != nil {
		return nil, err
	}
	return testobservation.VerifyOCI(ctx, reference, expected, options)
}
