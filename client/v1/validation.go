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

// ValidatorDescription, ValidatorInvocation and ValidatorRuntime are provisional
// diagnostic surfaces. They carry no authentication or qualification authority.
type ValidatorDescription = validation.Registration
type ValidatorInvocation = validation.Invocation
type ValidatorRuntime = validation.Runtime

// ListValidators reads the registrations beneath one catalog directory without runtime probes.
func (c *Client) ListValidators(ctx context.Context, catalog string) ([]ValidatorDescription, error) {
	if err := c.ready(ctx, "validator discovery"); err != nil {
		return nil, err
	}
	registrations, err := validation.LoadRegistrations(ctx, catalog)
	if err != nil {
		return nil, err
	}
	return registrations.List(), nil
}

// DescribeValidator requires an exact ID in one catalog directory.
func (c *Client) DescribeValidator(ctx context.Context, catalog, id string) (ValidatorDescription, error) {
	if err := c.ready(ctx, "validator discovery"); err != nil {
		return ValidatorDescription{}, err
	}
	registrations, err := validation.LoadRegistrations(ctx, catalog)
	if err != nil {
		return ValidatorDescription{}, err
	}
	return registrations.Describe(id)
}

// PrepareValidatorProfile freezes a versioned local diagnostic selection before work.
func (c *Client) PrepareValidatorProfile(ctx context.Context, path string, runtime ValidatorRuntime) (*ValidatorInvocation, error) {
	if err := c.ready(ctx, "validator admission"); err != nil {
		return nil, err
	}
	return validation.AdmitProfile(ctx, path, runtime)
}
