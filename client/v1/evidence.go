// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// Evidence types are aliases of their authoritative owner, not competing schemas.
type EvidenceExpected = testobservation.Expected
type EvidenceRegistryOptions = testobservation.RegistryOptions
type EvidencePublished = testobservation.Published
type VerifiedEvidence = testobservation.Verified
type EvidenceLocalVerification = testobservation.LocalVerification

// EmitEvidenceRequest selects reviewed input files and a new output bundle.
type EmitEvidenceRequest struct {
	Root, DescriptionPath, SelectionPath, OutputPath string
}

// VerifyEvidenceRequest supplies independent expectations and one locator or reference.
type VerifyEvidenceRequest struct {
	Reference, LocatorPath, ExpectedPath string
}

// EmitEvidence captures only selected reviewed files and returns descriptive identity.
func (c *Client) EmitEvidence(ctx context.Context, request EmitEvidenceRequest) (string, error) {
	if err := c.ready(ctx, "emitting test evidence"); err != nil {
		return "", err
	}
	return testobservation.EmitFiles(ctx, request.Root, request.DescriptionPath, request.SelectionPath, request.OutputPath)
}

// PublishEvidence uploads and signs exact recorded bytes; test success is separate.
func (c *Client) PublishEvidence(ctx context.Context, root, repository string, options testobservation.RegistryOptions) (*testobservation.Published, error) {
	if err := c.ready(ctx, "publishing test evidence"); err != nil {
		return nil, err
	}
	return testobservation.Publish(ctx, root, repository, options)
}

// VerifyEvidence authenticates independently selected exact subjects and policy.
func (c *Client) VerifyEvidence(ctx context.Context, reference string, expected testobservation.Expected, options testobservation.RegistryOptions) (*testobservation.Verified, error) {
	if err := c.ready(ctx, "verifying test evidence"); err != nil {
		return nil, err
	}
	return testobservation.VerifyOCI(ctx, reference, expected, options)
}

// VerifyLocalEvidence returns unsigned historical self-consistency diagnostics.
func (c *Client) VerifyLocalEvidence(ctx context.Context, root, expectedPath string) (*EvidenceLocalVerification, error) {
	if err := c.ready(ctx, "inspecting unsigned test evidence"); err != nil {
		return nil, err
	}
	return testobservation.VerifyLocalFiles(ctx, root, expectedPath)
}

// VerifyEvidenceFiles delegates bounded policy and locator loading after admission.
// The caller owns and must close the returned live authenticated capability.
func (c *Client) VerifyEvidenceFiles(ctx context.Context, request VerifyEvidenceRequest, options EvidenceRegistryOptions) (*VerifiedEvidence, error) {
	if err := c.ready(ctx, "authenticating configured test evidence"); err != nil {
		return nil, err
	}
	return testobservation.VerifyFiles(ctx, request.Reference, request.LocatorPath, request.ExpectedPath, options)
}
