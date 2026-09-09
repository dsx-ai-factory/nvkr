package factory

// executors_sbom.go owns the per-recipe SBOM node. ADR-13 §1 (re-entered into v1
// by ADR-13) requires a deterministic CycloneDX SBOM per built artifact plus
// an aggregate index. pkg/nkd/sbom owns the document shape and its validation;
// what this file owns is the JOIN: reading each built component's observed digest
// from the producer-result this recipe already wrote, pairing it with the plan's
// persisted resolved composition, and refusing to emit a subject the run never
// observed. It signs nothing and asserts composition only — the output is
// static-unqualified evidence, never a signature or a reproduction claim.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
)

func executeSBOM(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer) error {
	var recipePlan *RecipePlan
	for i := range plan.Recipes {
		if plan.Recipes[i].Name == node.RecipeTarget {
			recipePlan = &plan.Recipes[i]
			break
		}
	}
	if recipePlan == nil {
		return e.New(e.CodePipelineDrift, "factory sbom node %q names unknown recipe target %q", node.ID, node.RecipeTarget)
	}
	if len(recipePlan.ResolvedComponents) == 0 {
		return e.New(e.CodePipelineDrift, "factory sbom node %q has no resolved components to bill", node.ID)
	}

	// Observed subject digests come only from producer-result inputs; the terminal
	// observation input is a scheduling gate, not a source of hashes.
	hashes := make(map[string]string)
	for _, input := range node.Inputs {
		artifact := artifacts[input]
		if artifact.Kind != "producer-result" {
			continue
		}
		result, err := readProducerResult(ctx, artifact.Path)
		if err != nil {
			return err
		}
		if result.Component == "" || !canonical.IsDigest(result.SHA256) {
			return e.New(e.CodePipelineDrift,
				"factory sbom node %q input %s carries no exact component digest", node.ID, input)
		}
		hashes[result.Component] = result.SHA256
	}

	resolved := &recipe.Resolved{
		RecipeID:   recipePlan.RecipeID,
		Criteria:   recipePlan.Criteria,
		Components: recipePlan.ResolvedComponents,
	}
	boms, index, err := sbom.PerArtifact(resolved, recipePlan.RecipeDigest, hashes)
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "factory sbom node %q", node.ID)
	}
	if len(boms) == 0 {
		return e.New(e.CodePipelineDrift, "factory sbom node %q produced no per-artifact SBOM", node.ID)
	}
	// Validate binds the index's exact subject and evidence digests before any bytes
	// are written: a per-artifact SBOM whose subject the run never observed fails here.
	if err := index.Validate(); err != nil {
		return err
	}

	dir := artifacts[node.Outputs[0]].Path
	for _, entry := range index.Entries {
		raw, err := boms[entry.Artifact].Marshal()
		if err != nil {
			return err
		}
		if err := writeFactoryFile(ctx, filepath.Join(dir, entry.Artifact+".cdx.json"), append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	rawIndex, err := canonical.Marshal(index)
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, filepath.Join(dir, "index.json"), append(rawIndex, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "billed %s: %d per-artifact SBOM(s) + index (static-unqualified)\n", node.ID, len(index.Entries))
	return nil
}

// readProducerResult admits the canonical pipeline.ArtifactResult that a build or
// pull node wrote for one component (persistRecipeResults marshaled it), so the
// observed digest the SBOM binds is the runner's, never a declaration.
func readProducerResult(ctx context.Context, path string) (pipeline.ArtifactResult, error) {
	raw, err := readSmallFactoryFile(ctx, path, "factory-producer-result")
	if err != nil {
		return pipeline.ArtifactResult{}, err
	}
	var result pipeline.ArtifactResult
	if err := canonical.UnmarshalExact(raw, &result); err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodePipelineDrift, err, "decoding producer result %s", path)
	}
	return result, nil
}
