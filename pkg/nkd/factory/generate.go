package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
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
	CompositionDir string
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
	workflowPath   string
	manifestPath   string
	planPath       string
	graphDir       string
	compositionDir string
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
	// The plan and composition members are read back at runtime with canonical.UnmarshalExact
	// (`factory execute-node --plan`, cluster composition loads), which rejects a trailing
	// newline. Write the canonical bytes verbatim, like the graph members below.
	files := map[string][]byte{layout.planPath: planBytes}
	for _, composition := range plan.Compositions {
		raw, err := composition.Composition.Marshal()
		if err != nil {
			return nil, "", nil, err
		}
		path := filepath.ToSlash(filepath.Join(layout.compositionDir, composition.Name+".json"))
		files[path] = raw
	}
	graphDigests := make(map[string]string, len(plan.Recipes))
	for _, target := range plan.Recipes {
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
		// canonical bytes verbatim (no trailing '\n', unlike the plan/composition members).
		files[path] = raw
	}
	return files, planDigest, graphDigests, nil
}

func newGeneratedBundle(engine string, layout generationLayout, files map[string][]byte, planDigest string) *GeneratedBundle {
	return &GeneratedBundle{Engine: engine, WorkflowPath: layout.workflowPath,
		ManifestPath: layout.manifestPath, PlanPath: layout.planPath,
		GraphDir: layout.graphDir, CompositionDir: layout.compositionDir,
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
	expectedFiles := map[string]bool{bundle.WorkflowPath: true, bundle.PlanPath: true}
	for _, recipe := range plan.Recipes {
		expectedFiles[filepath.ToSlash(filepath.Join(bundle.GraphDir, recipe.Name+".json"))] = true
	}
	for _, composition := range plan.Compositions {
		expectedFiles[filepath.ToSlash(filepath.Join(bundle.CompositionDir, composition.Name+".json"))] = true
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
	if err != nil || digest != bundle.PlanDigest {
		return nil, e.New(e.CodePipelineDrift, "generated factory plan does not match planDigest")
	}
	for _, recipe := range plan.Recipes {
		path := filepath.ToSlash(filepath.Join(bundle.GraphDir, recipe.Name+".json"))
		raw, ok := bundle.Files[path]
		if !ok {
			return nil, e.New(e.CodePipelineDrift, "%s bundle omits graph for target %q", bundle.Engine, recipe.Name)
		}
		var graph ir.Graph
		if err := canonical.UnmarshalExact(bytes.TrimSpace(raw), &graph); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decoding generated graph for %q", recipe.Name)
		}
		if err := graph.Validate(); err != nil {
			return nil, err
		}
		if got, _ := graph.Digest(); got != recipe.RecipeDigest {
			return nil, e.New(e.CodePipelineDrift, "generated graph for %q changed recipe identity", recipe.Name)
		}
	}
	for _, composition := range plan.Compositions {
		path := filepath.ToSlash(filepath.Join(bundle.CompositionDir, composition.Name+".json"))
		raw, ok := bundle.Files[path]
		if !ok {
			return nil, e.New(e.CodePipelineDrift, "%s bundle omits composition %q", bundle.Engine, composition.Name)
		}
		var decoded cluster.ClusterComposition
		if err := canonical.UnmarshalExact(bytes.TrimSpace(raw), &decoded); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decoding generated composition %q", composition.Name)
		}
		if digest, err := decoded.Digest(); err != nil || digest != composition.Digest {
			return nil, e.New(e.CodePipelineDrift, "generated composition %q changed identity", composition.Name)
		}
	}
	return workflow, nil
}
