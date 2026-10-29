// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// CompileFiles admits authored inputs and delegates their complete compilation.
// A composite compiles through bounded capture beneath the working directory,
// its admitted input root, so every member and catalog read stays confined.
func CompileFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion string) (*factory.Plan, error) {
	kind, err := definition.ReadDocumentKind(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	if kind == definition.CompositeKind {
		plan, _, err := captureComposite(ctx, definitionPath, environmentPath, generatorVersion)
		return plan, err
	}
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

// Compile returns the compiler's complete plan without a lifecycle enrichment pass.
func Compile(ctx context.Context, definitionPath string, definition Definition, environmentPath string, environment Environment, generatorVersion string) (*factory.Plan, error) {
	return factory.CompileDocuments(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
}

const publicationPackageName = "release"
