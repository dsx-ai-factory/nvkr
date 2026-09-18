package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
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
	GraphDir       string
	Files          map[string][]byte
	WorkflowDigest string
	PlanDigest     string
}

// ValidateGenerationEngine performs capability admission without reading factory
// inputs. It is intentionally callable before catalog resolution.
func ValidateGenerationEngine(engine string) error {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case EngineGitHub, EngineGitLab:
		return nil
	case EngineTekton, EngineArgo:
		return e.New(e.CodeEngineCapability,
			"factory generation for %s is unavailable until its adapter transports every named artifact edge", engine)
	case "local":
		return e.New(e.CodeUsage, "local execution uses `run local`, not `run generate`")
	default:
		return e.New(e.CodeUsage, "unknown factory generation engine %q (github|gitlab)", engine)
	}
}

// Render selects a concrete topology/transport adapter at the engine seam.
func Render(ctx context.Context, plan *Plan, engine string) (*GeneratedBundle, error) {
	if err := ValidateGenerationEngine(engine); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case EngineGitHub:
		return RenderGitHub(ctx, plan)
	case EngineGitLab:
		return RenderGitLab(ctx, plan)
	default:
		return nil, e.New(e.CodeInternal, "factory generation engine %q has no adapter", engine)
	}
}

// ValidateGeneratedBundle reruns the selected adapter's semantic static analysis.
func ValidateGeneratedBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	if bundle == nil {
		return e.New(e.CodeUsage, "factory static validation requires a generated bundle")
	}
	switch bundle.Engine {
	case EngineGitHub:
		return ValidateGitHubBundle(ctx, plan, bundle)
	case EngineGitLab:
		return ValidateGitLabBundle(ctx, plan, bundle)
	default:
		return e.New(e.CodeUsage, "generated factory bundle has unknown engine %q", bundle.Engine)
	}
}

func generationCommand(plan *Plan, engine string) string {
	return "./nkd-exec run generate --engine " + engine +
		" --definition " + shellQuote(plan.DefinitionPath) +
		" --environment " + shellQuote(plan.EnvironmentPath) + " --root . --check"
}

type generationLayout struct {
	workflowPath string
	manifestPath string
	planPath     string
	graphDir     string
}

func renderStaticInputs(ctx context.Context, plan *Plan, layout generationLayout) (map[string][]byte, string, map[string]string, error) {
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, "", nil, err
	}
	planBytes, err := canonical.Marshal(plan)
	if err != nil {
		return nil, "", nil, err
	}
	// Runtime reads canonical plan bytes exactly, without a trailing newline.
	files := map[string][]byte{layout.planPath: planBytes}
	graphDigests := make(map[string]string, len(plan.Builds))
	for _, target := range plan.Builds {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, e.Wrap(e.CodeTimeout, err, "factory rendering canceled")
		}
		raw, err := target.Graph.CanonicalJSON()
		if err != nil {
			return nil, "", nil, err
		}
		path := filepath.ToSlash(filepath.Join(layout.graphDir, target.Name+".json"))
		graphDigests[target.Name] = pipeline.FileDigest(raw)
		// The committed graph file MUST be exactly the bytes hashed above: at runtime
		// `nkd-exec exec-node --nodes-digest` re-hashes the file it reads and fails closed on
		// any mismatch, and canonical.UnmarshalExact rejects a trailing newline. So write the
		// canonical bytes verbatim (no trailing '\n', like the plan members).
		files[path] = raw
	}
	return files, planDigest, graphDigests, nil
}

func newGeneratedBundle(engine string, layout generationLayout, files map[string][]byte, planDigest string) *GeneratedBundle {
	return &GeneratedBundle{Engine: engine, WorkflowPath: layout.workflowPath,
		ManifestPath: layout.manifestPath, PlanPath: layout.planPath,
		GraphDir: layout.graphDir,
		Files:    files, WorkflowDigest: pipeline.FileDigest(files[layout.workflowPath]), PlanDigest: planDigest}
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
	if err := rejectUnsupportedPlanRequirements(plan, bundle.Engine+" bundle validation"); err != nil {
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
	expectedFiles := map[string]bool{bundle.WorkflowPath: true, bundle.PlanPath: true}
	for _, build := range plan.Builds {
		expectedFiles[filepath.ToSlash(filepath.Join(bundle.GraphDir, build.Name+".json"))] = true
	}
	if len(bundle.Files) != len(expectedFiles) {
		return nil, e.New(e.CodePipelineDrift, "%s bundle has %d files; expected %d", bundle.Engine, len(bundle.Files), len(expectedFiles))
	}
	for name := range bundle.Files {
		if !expectedFiles[name] {
			return nil, e.New(e.CodePipelineDrift, "%s bundle contains unexpected file %q", bundle.Engine, name)
		}
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
	if err := canonical.UnmarshalExact(bytes.TrimSpace(planBytes), &decodedPlan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding generated factory plan")
	}
	digest, err := decodedPlan.Digest()
	if err != nil || digest != expectedPlanDigest {
		return nil, e.New(e.CodePipelineDrift, "generated factory plan does not match planDigest")
	}
	if err := rejectUnsupportedPlanRequirements(&decodedPlan, bundle.Engine+" bundle validation"); err != nil {
		return nil, err
	}
	for _, build := range plan.Builds {
		path := filepath.ToSlash(filepath.Join(bundle.GraphDir, build.Name+".json"))
		raw, ok := bundle.Files[path]
		if !ok {
			return nil, e.New(e.CodePipelineDrift, "%s bundle omits graph for target %q", bundle.Engine, build.Name)
		}
		expectedGraph, err := build.Graph.CanonicalJSON()
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(raw, expectedGraph) {
			return nil, e.New(e.CodePipelineDrift,
				"generated graph for %q does not match the authoritative full graph", build.Name)
		}
		var graph ir.Graph
		if err := canonical.UnmarshalExact(raw, &graph); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decoding generated graph for %q", build.Name)
		}
		if err := graph.Validate(); err != nil {
			return nil, err
		}
		if got, _ := graph.Digest(); got != build.BuildDigest {
			return nil, e.New(e.CodePipelineDrift, "generated graph for %q changed build identity", build.Name)
		}
	}
	return workflow, nil
}
