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

// Render selects a concrete topology/transport adapter at the engine seam. It
// is the top-level generation seam: only `run generate` writes, its diagnostic
// projection-pilot check, qualification generation and catalog maintenance
// call it. Generated bootstrap and worker
// commands admit committed bytes through CheckGenerated and the GitHub runtime
// owners, which use only the core projection, so no generator selected here
// runs inside a generated stage.
func Render(ctx context.Context, plan *Plan, engine string) (*GeneratedBundle, error) {
	if err := ValidateGenerationEngine(engine); err != nil {
		return nil, err
	}
	return renderSelected(ctx, plan)
}

// renderSelected is the projection behind Render: the in-binary GitHub renderer,
// or the projection plugin and transport a top-level caller selected through
// WithProjectionAdapter. Boundary tests trap it.
var renderSelected = func(ctx context.Context, plan *Plan) (*GeneratedBundle, error) {
	if adapter, ok := selectedProjectionAdapter(ctx); ok {
		plugin, err := adapter.plugin()
		if err != nil {
			return nil, err
		}
		return renderProjected(ctx, plan, plugin, adapter.Release)
	}
	return RenderGitHub(ctx, plan)
}

// CheckGenerated is the check twin of Render that never takes the generation
// seam: it compares the committed bundle under root with the core projection of
// plan. Generated bootstraps run it through `run generate --check`.
func CheckGenerated(ctx context.Context, plan *Plan, engine, root string) error {
	if err := ValidateGenerationEngine(engine); err != nil {
		return err
	}
	_, err := checkCoreGitHubBundle(ctx, plan, root)
	return err
}

// Generate checks or writes one hosted bundle and returns its Plan digest.
// Generated bootstraps run check mode, so only write mode takes the Render seam.
// The one exception is a top-level diagnostic check that selected a projection
// adapter through WithProjectionAdapter; no generated command can select one.
func Generate(ctx context.Context, plan *Plan, engine, root string, check bool) (string, error) {
	if check && !ProjectionAdapterSelected(ctx) {
		if err := CheckGenerated(ctx, plan, engine, root); err != nil {
			return "", err
		}
		return plan.Digest()
	}
	bundle, err := Render(ctx, plan, engine)
	if err != nil {
		return "", err
	}
	if check {
		if err := CheckGeneratedBundle(ctx, root, bundle); err != nil {
			return "", err
		}
		return bundle.PlanDigest, nil
	}
	if err := WriteGeneratedBundle(ctx, root, bundle); err != nil {
		return "", err
	}
	return bundle.PlanDigest, nil
}

// checkCoreGitHubBundle admits exact committed bytes against the core's
// in-binary projection and returns that projection for further comparison.
func checkCoreGitHubBundle(ctx context.Context, plan *Plan, root string) (*GeneratedBundle, error) {
	bundle, err := RenderGitHub(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := CheckGeneratedBundle(ctx, root, bundle); err != nil {
		return nil, err
	}
	return bundle, nil
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
	layout := githubLayoutFor(plan)
	if bundle.Engine != EngineGitHub || bundle.WorkflowPath != layout.workflowPath ||
		bundle.ManifestPath != layout.manifestPath || bundle.PlanPath != layout.planPath {
		return nil, e.New(e.CodePipelineDrift, "generated bundle paths do not match the admitted Environment")
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
