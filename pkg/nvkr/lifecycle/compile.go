// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// CompileFiles admits authored inputs and delegates their complete compilation.
func CompileFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion string) (*factory.Plan, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	return Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
}

// CompileRuntimeSourceFiles re-authenticates the exact frozen repository tree
// and recompiles the current authored selection through its owning modules.
func CompileRuntimeSourceFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, commit string) (*factory.Plan, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	return factory.CompileRuntimeSourceDocuments(ctx, definitionPath, definition, environmentPath, environment, generatorVersion, commit)
}

// Compile returns the compiler's complete plan without a lifecycle enrichment pass.
func Compile(ctx context.Context, definitionPath string, definition Definition, environmentPath string, environment Environment, generatorVersion string) (*factory.Plan, error) {
	return factory.CompileDocuments(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
}

const publicationPackageName = "release"
