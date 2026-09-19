package factory

// execute_sbom.go owns the per-build SBOM node. ADR-13 §1 (re-entered into v1
// by ADR-13) requires a deterministic CycloneDX SBOM per built artifact plus
// an aggregate index. pkg/nkd/sbom owns the document shape and its validation;
// what this file owns is the JOIN: reading each built component's observed digest
// from the producer-result this build already wrote, pairing it with the plan's
// persisted resolved composition, and refusing to emit a subject the run never
// observed. It signs nothing and asserts composition only — the output is
// static-unqualified evidence, never a signature or a reproduction claim.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func executeSBOM(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer) error {
	var buildPlan *BuildPlan
	for i := range plan.Builds {
		if plan.Builds[i].Name == node.BuildTarget {
			buildPlan = &plan.Builds[i]
			break
		}
	}
	if buildPlan == nil {
		return e.New(e.CodePipelineDrift, "factory sbom node %q names unknown build target %q", node.ID, node.BuildTarget)
	}
	if len(buildPlan.ResolvedComponents) == 0 {
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
		if result.Component == "" || result.Component != artifact.Component || artifact.BuildTarget != node.BuildTarget || hashes[result.Component] != "" || !canonical.IsDigest(result.SHA256) {
			return e.New(e.CodePipelineDrift,
				"factory sbom node %q input %s carries no exact component digest", node.ID, input)
		}
		hashes[result.Component] = result.SHA256
	}

	resolved := &buildgraph.Resolved{
		Platform:   buildPlan.Platform,
		Components: buildPlan.ResolvedComponents,
	}
	boms, index, err := sbom.PerArtifact(resolved, buildPlan.BuildDigest, hashes)
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "factory sbom node %q", node.ID)
	}
	if len(boms) == 0 {
		return e.New(e.CodePipelineDrift, "factory sbom node %q produced no per-artifact SBOM", node.ID)
	}
	if tool := plan.Definition.SBOMTool; tool != "" {
		if !containsString(node.Inputs, "tool/"+tool) || artifacts["tool/"+tool].Kind != "pipeline-tool" {
			return e.New(e.CodeProvenanceMissing, "SBOM node omits pinned scanner input")
		}
		scanner := sbom.Scanner{Path: artifacts["tool/"+tool].Path, Digest: toolExecutableDigest(plan, tool)}
		for i := range index.Entries {
			entry := &index.Entries[i]
			var payload Artifact
			for _, input := range node.Inputs {
				candidate := artifacts[input]
				if candidate.Component == entry.Artifact && candidate.BuildTarget == node.BuildTarget && candidate.Kind != "producer-result" {
					if payload.ID != "" {
						return e.New(e.CodeProvenanceMissing, "ambiguous SBOM payload for %q", entry.Artifact)
					}
					payload = candidate
				}
			}
			if payload.ID == "" {
				return e.New(e.CodeProvenanceMissing, "missing SBOM payload for %q", entry.Artifact)
			}
			inventory, err := sbom.InspectDependencies(ctx, payload.Path, entry.ArtifactSHA, payload.Kind, scanner)
			if err != nil {
				return err
			}
			bom, err := sbom.WithDependencies(boms[entry.Artifact], inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				return err
			}
			boms[entry.Artifact] = bom
			entry.SBOMDigest, err = bom.Digest()
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Inspected %s: %d dependency components (%s)\n", entry.Artifact, len(bom.Components), inventory.Method)
		}
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
	fmt.Fprintf(stdout, "Generated %s: %d per-artifact SBOM(s) + index (composition evidence; not qualification)\n", node.ID, len(index.Entries))
	return nil
}

func (plan *Plan) validateSBOMGraph() error {
	tool := plan.Definition.SBOMTool
	if tool == "" {
		return nil
	}
	nodes, artifacts := planIndex(plan)
	scanner, ok := nodes["tool/"+tool]
	if !ok || scanner.Kind != NodeAcquire || !canonical.IsDigest(scanner.ExecutableDigest) || artifacts["tool/"+tool].Kind != "pipeline-tool" {
		return e.New(e.CodeProvenanceMissing, "SBOM inspection requires a pinned acquired scanner")
	}
	for _, artifact := range artifacts {
		if artifact.Kind != "producer-result" {
			continue
		}
		if plan.hasCompiledDiskTarget(artifact.BuildTarget, artifacts) {
			// Disk targets require ADR-13's future disk inspection and material
			// reconciliation operation, not this ordinary per-component SBOM.
			continue
		}
		node, ok := nodes[artifact.BuildTarget+"/sbom"]
		if !ok || node.Kind != NodeSBOM || node.BuildTarget != artifact.BuildTarget || !containsString(node.Inputs, artifact.ID) {
			return e.New(e.CodeProvenanceMissing, "dependency SBOM graph omits producer %q", artifact.ID)
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeSBOM {
			continue
		}
		if node.Network != ir.NetworkNone || node.Container == "" || !containsString(node.Inputs, "tool/"+tool) {
			return e.New(e.CodeEngineCapability, "SBOM inspection must be offline with explicit scanner input")
		}
		for _, input := range node.Inputs {
			result := artifacts[input]
			if result.Kind != "producer-result" {
				continue
			}
			matches := 0
			for _, candidateID := range node.Inputs {
				candidate := artifacts[candidateID]
				if candidate.Component == result.Component && candidate.BuildTarget == node.BuildTarget && candidate.Kind != "producer-result" {
					if candidate.Kind != "host-package" && candidate.Kind != "oci-image" && candidate.Kind != "tarball" {
						return e.New(e.CodeEngineCapability, "SBOM dependency inspection does not support %q", candidate.Kind)
					}
					matches++
				}
			}
			if matches != 1 {
				return e.New(e.CodeProvenanceMissing, "SBOM inspection requires one exact payload for %q", result.Component)
			}
		}
	}
	return nil
}

// hasCompiledDiskTarget recognizes only the compiler-bound disk lifecycle shape.
// Definition format alone is insufficient: the lifecycle binding, raw artifact,
// resolved root, and typed VM IR producer must all name the same target/root.
func (plan *Plan) hasCompiledDiskTarget(target string, artifacts map[string]Artifact) bool {
	var outputName string
	for _, output := range plan.Definition.Outputs {
		if output.Name == target && output.Format == authored.OutputDiskImage {
			outputName = output.Name
			break
		}
	}
	if outputName == "" {
		return false
	}
	var binding LifecycleOutputPlan
	for _, candidate := range plan.LifecycleOutputs {
		if candidate.Name == outputName && candidate.Kind == authored.OutputDiskImage {
			binding = candidate
			break
		}
	}
	payload, ok := artifacts[binding.ArtifactID]
	if binding.ArtifactID == "" || !ok || payload.Kind != authored.OutputDiskImage ||
		payload.BuildTarget != target || payload.Component == "" {
		return false
	}
	var build *BuildPlan
	for index := range plan.Builds {
		if plan.Builds[index].Name == target {
			build = &plan.Builds[index]
			break
		}
	}
	if build == nil || build.Graph == nil {
		return false
	}
	rootCount := 0
	for _, component := range build.ResolvedComponents {
		if component.OutputType == authored.OutputDiskImage {
			rootCount++
			if component.Name != payload.Component {
				return false
			}
		}
	}
	if rootCount != 1 {
		return false
	}
	for _, node := range build.Graph.Nodes {
		if node.Component == payload.Component && node.Kind == ir.KindBuild &&
			node.BuilderType == ir.BuilderVMImage && node.OutputType == authored.OutputDiskImage &&
			node.Disk != nil && node.Disk.Format == "qcow2" &&
			payload.Producer == target+"/"+node.ID {
			return true
		}
	}
	return false
}

// readProducerResult admits the canonical pipeline.ArtifactResult that a build or
// pull operation wrote for one exact component, so the
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
