// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"sort"
	"strings"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func addBuildDataflow(targetSpec authored.DefinitionOutput, runsOn string, graph *ir.Graph, selected map[string]bool,
	utilityContainer, utilityPlatform string, diskPublication bool, nodes map[string]Node, artifacts map[string]Artifact) error {
	target := targetSpec.Name
	byID := map[string]ir.Node{}
	selectedFetch := map[string]bool{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild || !selected[node.Component] {
			continue
		}
		for _, dependency := range node.Needs {
			if byID[dependency].Kind == ir.KindFetch {
				selectedFetch[dependency] = true
			}
		}
	}
	producerOutputs := make([]string, 0, len(selected))
	for _, irNode := range graph.Nodes {
		nodeID := target + "/" + irNode.ID
		switch irNode.Kind {
		case ir.KindFetch:
			if !selectedFetch[irNode.ID] {
				continue
			}
			output := "source/" + target + "/" + irNode.ID
			receipt := "source-receipt/" + target + "/" + irNode.ID
			receiptPath, err := pipeline.SourceReceiptPath(filepath.Join(".nvkr", "factory", target, "src"), irNode.Dest)
			if err != nil {
				return err
			}
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeFetch, BuildTarget: target, IRNode: irNode.ID,
				Outputs: []string{output, receipt}}
			artifacts[output] = Artifact{ID: output, Name: irNode.ID, Kind: "source-closure",
				Producer: nodeID, BuildTarget: target,
				Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "src", irNode.Dest))}
			artifacts[receipt] = Artifact{ID: receipt, Name: irNode.ID + "-receipt", Kind: "source-receipt",
				Producer: nodeID, BuildTarget: target,
				Path: filepath.ToSlash(receiptPath)}
		case ir.KindBuild:
			if !selected[irNode.Component] {
				continue
			}
			inputs := make([]string, 0, len(irNode.Needs))
			for _, dependency := range irNode.Needs {
				upstream, ok := byID[dependency]
				if !ok {
					return e.New(e.CodePipelineDrift,
						"build %q references absent producer input %q", irNode.ID, dependency)
				}
				switch upstream.Kind {
				case ir.KindFetch:
					inputs = append(inputs, "source/"+target+"/"+dependency, "source-receipt/"+target+"/"+dependency)
				case ir.KindBuild, ir.KindPull:
					inputs = append(inputs,
						"artifact/"+target+"/"+upstream.Component,
						"result/"+target+"/"+upstream.Component)
				default:
					return e.New(e.CodeInvalidCatalog,
						"build %q has unsupported producer input %q", irNode.ID, dependency)
				}
			}
			output := "artifact/" + target + "/" + irNode.Component
			result := "result/" + target + "/" + irNode.Component
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeBuild, BuildTarget: target, IRNode: irNode.ID,
				Inputs: inputs, Outputs: []string{output, result}}
			artifacts[output] = Artifact{ID: output, Name: irNode.Component, Kind: irNode.OutputType,
				MediaType: artifactMediaType(irNode.OutputType), Producer: nodeID, BuildTarget: target,
				Component: irNode.Component, Path: factoryBuildArtifactPath(target, irNode)}
			artifacts[result] = Artifact{ID: result, Name: irNode.Component + "-result", Kind: "producer-result",
				Producer: nodeID, BuildTarget: target, Component: irNode.Component,
				Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "results", irNode.ID+".json"))}
			producerOutputs = append(producerOutputs, output, result)
		case ir.KindPull:
			if !selected[irNode.Component] {
				continue
			}
			output := "artifact/" + target + "/" + irNode.Component
			result := "result/" + target + "/" + irNode.Component
			nodes[nodeID] = Node{ID: nodeID, Kind: NodeAcquire, BuildTarget: target, IRNode: irNode.ID,
				Outputs: []string{output, result}}
			artifacts[output] = Artifact{ID: output, Name: irNode.Component, Kind: irNode.OutputType,
				MediaType: artifactMediaType(irNode.OutputType), Producer: nodeID, BuildTarget: target,
				Component: irNode.Component, Path: factoryPullArtifactPath(target, irNode)}
			artifacts[result] = Artifact{ID: result, Name: irNode.Component + "-result", Kind: "producer-result",
				Producer: nodeID, BuildTarget: target, Component: irNode.Component,
				Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "results", irNode.ID+".json"))}
			producerOutputs = append(producerOutputs, output, result)
		}
	}
	sort.Strings(producerOutputs)
	if len(producerOutputs) == 0 {
		return e.New(e.CodeInvalidCatalog, "factory build target %q has no selected artifact producers", target)
	}
	verifyID := target + "/terminal-observe"
	observationID := "observation/" + target
	nodes[verifyID] = Node{ID: verifyID, Kind: NodeVerify, BuildTarget: target,
		Network: ir.NetworkNone, RunsOn: runsOn, Container: utilityContainer,
		Platform: utilityPlatform, Inputs: producerOutputs, Outputs: []string{observationID}}
	artifacts[observationID] = Artifact{ID: observationID, Name: target + "-terminal-observation",
		Kind: string(TierTerminalObservation), Producer: verifyID, BuildTarget: target,
		Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "terminal-observation.json"))}
	if targetSpec.Format == authored.OutputDiskImage && !diskPublication {
		// The ordinary per-component SBOM operation does not prove the disk
		// inventory and material reconciliation required by ADR-13. Diagnostic-only
		// plans retain their raw disk without claiming a complete
		// SBOM. Production plans require the separate live reconciliation operation.
		return nil
	}

	// SBOM node (ADR-13 §1): one per build target, gated on the
	// terminal observation, binding a CycloneDX SBOM per built component to that
	// component's producer-result digest. It bills only what was built.
	resultInputs := make([]string, 0, len(producerOutputs))
	for _, id := range producerOutputs {
		if strings.HasPrefix(id, "result/") {
			resultInputs = append(resultInputs, id)
		}
	}
	if len(resultInputs) > 0 {
		sbomNodeID := target + "/sbom"
		sbomArtifactID := "sbom/" + target
		inputs := uniqueSorted(append(resultInputs, observationID))
		nodes[sbomNodeID] = Node{ID: sbomNodeID, Kind: NodeSBOM, BuildTarget: target,
			Network: ir.NetworkNone, RunsOn: runsOn, Container: utilityContainer,
			Platform: utilityPlatform, Inputs: inputs, Outputs: []string{sbomArtifactID}}
		artifacts[sbomArtifactID] = Artifact{ID: sbomArtifactID, Name: target + "-sbom",
			Kind: "sbom-set", MediaType: "application/vnd.nvkr.sbom-set.v1+json",
			Producer: sbomNodeID, BuildTarget: target,
			Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "sbom"))}
	}
	return nil
}

func factoryBuildArtifactPath(target string, node ir.Node) string {
	rel := filepath.Clean(node.ArtifactPath)
	if node.WriteDir != "" {
		if candidate, err := filepath.Rel(filepath.Clean(node.WriteDir), rel); err == nil &&
			candidate != ".." && !strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
			rel = candidate
		}
	}
	return filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "out", node.Component, rel))
}

func factoryPullArtifactPath(target string, node ir.Node) string {
	if node.OutputType == "oci-image" {
		return filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "fetch", node.Component, node.Component+".oci"))
	}
	name := node.ArtifactPath
	if name == "" {
		name = node.Component
	}
	return filepath.ToSlash(filepath.Join(".nvkr", "factory", target, "fetch", node.Component, name))
}
