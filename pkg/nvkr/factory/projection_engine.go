// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// projectionCore binds an admitted engine to its core implementation. Neither
// a selected plugin nor its response can choose the layout or validator.
type projectionCore struct {
	engine   string
	layout   func(*Plan) generationLayout
	render   func(context.Context, *Plan) (*GeneratedBundle, error)
	validate func(context.Context, *Plan, *GeneratedBundle) error
}

func projectionCoreForEngine(engine string) (projectionCore, error) {
	if err := ValidateGenerationEngine(engine); err != nil {
		return projectionCore{}, err
	}
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case EngineTekton:
		return projectionCore{EngineTekton, tektonLayoutFor, renderTekton, ValidateTektonBundle}, nil
	case EngineGitHub:
		return projectionCore{EngineGitHub, githubLayoutFor, RenderGitHub, ValidateGitHubBundle}, nil
	default:
		return projectionCore{}, e.New(e.CodeEngineCapability, "engine %q has no projection contract", engine)
	}
}

func tektonLayoutFor(*Plan) generationLayout {
	return generationLayout{workflowPath: tektonResourcePath, manifestPath: tektonManifestPath, planPath: tektonPlanPath}
}

// ValidateTektonBundle admits exactly the core's finite projection of plan.
// It never selects a plugin, writes output or contacts the cluster.
func ValidateTektonBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	if bundle == nil {
		return e.New(e.CodePipelineDrift, "Tekton projection omitted its bundle")
	}
	expected, err := renderTekton(ctx, plan)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(bundle, expected) {
		return e.New(e.CodePipelineDrift, "Tekton bundle differs from its exact core projection")
	}
	return nil
}
