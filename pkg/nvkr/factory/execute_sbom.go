// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

// execute_sbom.go owns the per-build SBOM node. ADR-13 §1 requires a
// deterministic CycloneDX SBOM per built artifact plus
// an aggregate index. pkg/nvkr/sbom owns the document shape and its validation;
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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
	if plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
		return e.New(e.CodeProvenanceMissing, "disk evidence requires the original live execution and reconciled receipt")
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
			kind, err := plan.sbomPayloadKind(payload)
			if err != nil {
				return err
			}
			var inventory sbom.DependencyInventory
			if kind == "oci-image" && planNode(plan, payload.Producer).Kind == NodeAcquire {
				inventory, err = inspectAcquiredImageDependencies(ctx, plan, node, payload, artifacts, entry.ArtifactSHA, scanner)
			} else {
				inventory, err = sbom.InspectDependencies(ctx, payload.Path, entry.ArtifactSHA, kind, scanner)
			}
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
		if len(raw) >= sbom.MaxDocumentBytes {
			return e.New(e.CodeVerifyFailed, "complete SBOM exceeds its newline-delimited file bound")
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
	nodes, artifacts := planIndex(plan)
	if tool != "" {
		scanner, ok := nodes["tool/"+tool]
		if !ok || scanner.Kind != NodeAcquire || !canonical.IsDigest(scanner.ExecutableDigest) || artifacts["tool/"+tool].Kind != "pipeline-tool" {
			return e.New(e.CodeProvenanceMissing, "SBOM inspection requires a pinned acquired scanner")
		}
	}
	for _, artifact := range artifacts {
		if artifact.Kind != "producer-result" {
			continue
		}
		disk := plan.hasCompiledDiskTarget(artifact.BuildTarget, artifacts)
		if tool == "" && !disk {
			continue
		}
		if disk {
			// Local raw diagnostics do not claim this evidence. Production keeps
			// every component slot, supplied by the same-attempt reconciliation.
			if plan.Environment.Publication == nil {
				continue
			}
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
		if plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
			if diskBuildProvenance(plan) {
				continue
			}
			var selected BuildPlan
			for _, build := range plan.Builds {
				if build.Name == node.BuildTarget {
					selected = build
				}
			}
			platform, profile, err := diskPublicationInspectionProfile(selected, plan.Environment)
			if err != nil {
				return err
			}
			if node.Container != profile.Image || node.Platform != platform || node.Network != ir.NetworkNone {
				return e.New(e.CodeEngineCapability, "disk SBOM requires its exact offline Plan-selected inspection profile")
			}
			continue
		}
		if tool == "" {
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
					kind, err := plan.sbomPayloadKind(candidate)
					if err != nil {
						return err
					}
					if kind != "host-package" && kind != "oci-image" && kind != "tarball" && kind != "file-archive" {
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
	root, err := buildgraph.DiskRootName(build.ResolvedComponents)
	if err != nil || root != payload.Component {
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

// sbomPayloadKind preserves the strict Helm reader and admits generic archive
// contents only for an exact tarball component selected in an executable bundle.
// Other wrappers can carry charts without Helm image mappings; their existing
// interpretation cannot be downgraded by a filename or a failed Helm parse.
func (plan *Plan) sbomPayloadKind(payload Artifact) (string, error) {
	if payload.Kind == "tarball" {
		for _, output := range plan.Definition.Outputs {
			if output.Name != payload.BuildTarget || output.Format != authored.OutputExecutable || !containsString(output.Components, payload.Component) {
				continue
			}
			for _, build := range plan.Builds {
				if build.Name != payload.BuildTarget {
					continue
				}
				for _, component := range build.ResolvedComponents {
					if component.Name != payload.Component {
						continue
					}
					if component.Helm != nil {
						return "tarball", nil
					}
					for _, producer := range plan.Graph.Nodes {
						if component.FromSource && component.OutputType == "tarball" && producer.ID == payload.Producer && producer.Kind == NodeBuild && producer.BuildTarget == payload.BuildTarget && containsString(producer.Outputs, payload.ID) {
							return "file-archive", nil
						}
					}
				}
			}
			return "", e.New(e.CodeEngineCapability, "archive SBOM payload %q is not an exact source-built executable bundle member", payload.ID)
		}
	}
	if payload.Kind != "raw" {
		return payload.Kind, nil
	}
	selected := false
	for _, output := range plan.Definition.Outputs {
		if output.Name == payload.BuildTarget && output.Format == authored.OutputHelm && len(output.Components) == 1 && output.Components[0] == payload.Component {
			selected = true
		}
	}
	acquired := false
	for _, node := range plan.Graph.Nodes {
		if node.ID == payload.Producer && node.Kind == NodeAcquire && node.BuildTarget == payload.BuildTarget && containsString(node.Outputs, payload.ID) {
			acquired = true
		}
	}
	for _, build := range plan.Builds {
		if build.Name != payload.BuildTarget {
			continue
		}
		for _, component := range build.ResolvedComponents {
			if selected && acquired && component.Name == payload.Component && !component.FromSource && component.Prebuilt != nil && component.OutputType == "raw" {
				return "tarball", nil
			}
		}
	}
	return "", e.New(e.CodeEngineCapability, "raw SBOM payload %q is not an acquired Helm output", payload.ID)
}
