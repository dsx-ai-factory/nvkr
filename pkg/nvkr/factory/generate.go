// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const (
	EngineGitHub = "github"
	EngineGitLab = "gitlab"
	EngineTekton = "tekton"
	EngineArgo   = "argo"
)

// GeneratedBundle is the complete finite output of one factory CI adapter.
// Publication is a separate transaction so adapters remain pure renderers.
type GeneratedBundle struct {
	Engine         string
	WorkflowPath   string
	ManifestPath   string
	PlanPath       string
	Files          map[string][]byte
	WorkflowDigest string
	PlanDigest     string
}

// ValidateGenerationEngine performs capability admission without reading factory
// inputs. It is intentionally callable before catalog resolution.
func ValidateGenerationEngine(engine string) error {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case EngineGitHub:
		return nil
	case EngineGitLab, EngineTekton, EngineArgo:
		return e.New(e.CodeEngineCapability,
			"factory generation for %s is unavailable; the supported hosted engine is github", engine)
	case "local":
		return e.New(e.CodeUsage, "local execution uses `run local`, not `run generate`")
	default:
		return e.New(e.CodeUsage, "unknown factory generation engine %q (github)", engine)
	}
}

// Render selects a concrete topology/transport adapter at the engine seam.
func Render(ctx context.Context, plan *Plan, engine string) (*GeneratedBundle, error) {
	if err := ValidateGenerationEngine(engine); err != nil {
		return nil, err
	}
	return RenderGitHub(ctx, plan)
}

func generationCommand(plan *Plan, engine string) string {
	return "./nvkr-exec run generate --engine " + engine +
		" --definition " + shellQuote(plan.DefinitionPath) +
		" --environment " + shellQuote(plan.EnvironmentPath) + " --root . --check"
}

type generationLayout struct {
	workflowPath string
	manifestPath string
	planPath     string
}

// The complete Plan owns every producer graph. Provider jobs all execute that
// single canonical document; standalone per-build graph files would duplicate
// executable inputs without a separate owner or consumer.
func renderStaticInputs(ctx context.Context, plan *Plan, layout generationLayout) (map[string][]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", e.Wrap(e.CodeTimeout, err, "factory rendering canceled")
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, "", err
	}
	planBytes, err := canonical.Marshal(plan)
	if err != nil {
		return nil, "", err
	}
	return map[string][]byte{layout.planPath: planBytes}, planDigest, nil
}

func newGeneratedBundle(engine string, layout generationLayout, files map[string][]byte, planDigest string) *GeneratedBundle {
	return &GeneratedBundle{Engine: engine, WorkflowPath: layout.workflowPath,
		ManifestPath: layout.manifestPath, PlanPath: layout.planPath,
		Files: files, WorkflowDigest: pipeline.FileDigest(files[layout.workflowPath]), PlanDigest: planDigest}
}

func validateGeneratedInputs(ctx context.Context, plan *Plan, bundle *GeneratedBundle) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "factory static validation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "factory static validation canceled")
	}
	if plan == nil || bundle == nil {
		return nil, e.New(e.CodeUsage, "factory static validation requires a plan and generated bundle")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if err := admitGitHubRequirements(plan, bundle.Engine+" bundle validation"); err != nil {
		return nil, err
	}
	expectedPlanDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if bundle.PlanDigest != expectedPlanDigest {
		return nil, e.New(e.CodePipelineDrift,
			"%s bundle planDigest does not match the authoritative plan", bundle.Engine)
	}
	if bundle.Engine != EngineGitHub || bundle.WorkflowPath != GitHubWorkflowPath || bundle.PlanPath != FactoryPlanPath || bundle.ManifestPath != GeneratedManifestPath {
		return nil, e.New(e.CodePipelineDrift, "generated Run bundle layout differs from the admitted GitHub layout")
	}
	workflow, ok := bundle.Files[bundle.WorkflowPath]
	if !ok || pipeline.FileDigest(workflow) != bundle.WorkflowDigest {
		return nil, e.New(e.CodePipelineDrift, "%s workflow bytes do not match workflowDigest", bundle.Engine)
	}
	planBytes, ok := bundle.Files[bundle.PlanPath]
	if !ok {
		return nil, e.New(e.CodePipelineDrift, "%s bundle omits %s", bundle.Engine, bundle.PlanPath)
	}
	var decodedPlan Plan
	if err := canonical.UnmarshalExact(planBytes, &decodedPlan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding generated factory plan")
	}
	digest, err := decodedPlan.Digest()
	if err != nil || digest != expectedPlanDigest {
		return nil, e.New(e.CodePipelineDrift, "generated factory plan does not match planDigest")
	}
	if err := admitGitHubRequirements(&decodedPlan, bundle.Engine+" bundle validation"); err != nil {
		return nil, err
	}

	return workflow, nil
}
