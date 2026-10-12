// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// The lifecycle aliases are the SDK's public product vocabulary. Compiler
// plans, graphs, inventories, and authority receipts remain behind methods.
type Definition = lifecycle.Definition
type Environment = lifecycle.Environment
type Run = lifecycle.Run
type Artifact = lifecycle.Artifact
type Release = lifecycle.Release
type VerificationPolicy = lifecycle.VerificationPolicy
type DiskEvidenceRequirement = lifecycle.DiskEvidenceRequirement
type ReleaseConsumerOptions = lifecycle.ConsumerOptions

const (
	DiskEvidenceStrict          = lifecycle.DiskEvidenceStrict
	DiskEvidenceBuildProvenance = lifecycle.DiskEvidenceBuildProvenance
)

type LocalRunOptions = lifecycle.LocalOptions
type PreparedRun = lifecycle.PreparedRun

func (c *Client) LoadDefinition(ctx context.Context, path string) (Definition, error) {
	if err := c.ready(ctx, "definition load"); err != nil {
		return Definition{}, err
	}
	return lifecycle.LoadDefinition(ctx, path)
}

func (c *Client) LoadEnvironment(ctx context.Context, path string) (Environment, error) {
	if err := c.ready(ctx, "environment load"); err != nil {
		return Environment{}, err
	}
	return lifecycle.LoadEnvironment(ctx, path)
}

// PrepareLifecycleRun resolves and freezes one new attempt without exposing its
// generated compiler plan to the caller.
func (c *Client) PrepareLifecycleRun(ctx context.Context, definitionPath, environmentPath, generatorVersion, attempt string) (*PreparedRun, error) {
	if err := c.ready(ctx, "lifecycle run preparation"); err != nil {
		return nil, err
	}
	return lifecycle.PrepareFiles(ctx, definitionPath, environmentPath, generatorVersion, attempt)
}

func (c *Client) ExecuteLifecycleRun(ctx context.Context, prepared *PreparedRun, options LocalRunOptions) (*Run, error) {
	if err := c.ready(ctx, "lifecycle run execution"); err != nil {
		return nil, err
	}
	if options.Runtime == "" {
		options.Runtime = c.runtime
	}
	return lifecycle.ExecuteLocal(ctx, prepared, options)
}

// GenerateLifecycleRun renders or checks one hosted run while keeping its
// generated Plan internal. The returned digest identifies that exact plan.
func (c *Client) GenerateLifecycleRun(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check bool) (string, error) {
	if err := c.ready(ctx, "lifecycle run generation"); err != nil {
		return "", err
	}
	return lifecycle.GenerateFiles(ctx, definitionPath, environmentPath, generatorVersion, engine, root, check)
}

func (c *Client) VerifyRelease(ctx context.Context, reference string, policy VerificationPolicy) (*lifecycle.VerifiedRelease, error) {
	if err := c.ready(ctx, "release verification"); err != nil {
		return nil, err
	}
	if c.lifecycleConsumer == nil {
		return nil, e.New(e.CodeEngineCapability, "release verification requires a configured consumer")
	}
	return c.lifecycleConsumer.Verify(ctx, reference, policy)
}
