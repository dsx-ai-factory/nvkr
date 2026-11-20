// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Source seeds are same-Run transport dependencies, not replacement logical
// producers. Every fetch keeps its original instruction, outputs and admission.
// One source owner acquires each repository/commit/tree. One owner per vendor
// specification and native platform prepares that closure; other consumers
// independently check it.
func githubSourceSeeds(plan *Plan, ordered []Node) (map[string]Node, error) {
	type pin struct{ repository, commit, tree string }
	type closure struct {
		pin
		vendor   string
		platform string
	}
	repositories := map[pin]Node{}
	closures := map[closure]Node{}
	seeds := map[string]Node{}
	for _, node := range ordered {
		if node.Kind != NodeFetch || !irProjectedNode(node) {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return nil, err
		}
		vendor, err := ir.VendorSpecIdentity(instruction.VendorCmd, instruction.VendorImage)
		if err != nil {
			return nil, err
		}
		key := pin{instruction.Repo, instruction.Commit, instruction.TreeHash}
		execution, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, err
		}
		if execution.Platform != "linux/amd64" && execution.Platform != "linux/arm64" {
			return nil, e.New(e.CodeEngineCapability, "shared source requires an admitted native source platform")
		}
		prepared := closure{key, vendor, execution.Platform}
		if seed, exists := closures[prepared]; exists {
			seeds[node.ID] = seed
			continue
		}
		closures[prepared] = node
		if seed, exists := repositories[key]; exists {
			seeds[node.ID] = seed
		} else {
			repositories[key] = node
		}
	}
	return seeds, nil
}

func githubSharedSourceProjection(plan *Plan, ordered []Node, keys map[string]string) ([]Node, error) {
	seeds, err := githubSourceSeeds(plan, ordered)
	if err != nil {
		return nil, err
	}
	graph := ArtifactGraph{Artifacts: slices.Clone(plan.Graph.Artifacts)}
	originals := make(map[string]Node, len(ordered))
	projectionKeys := make(map[string]string, len(keys))
	grouping := authorityStages{plan: plan}
	for _, node := range ordered {
		projectionKeys[node.ID] = keys[node.ID]
		if grouping.imagePublication(node) {
			// Drain one output's chain before starting the next. This bounds
			// payload fan-in while retaining every existing operation budget.
			projectionKeys[node.ID] = "publication/" + node.Target + "/" + keys[node.ID]
		}
	}
	for _, node := range ordered {
		if seed, exists := seeds[node.ID]; exists {
			node.Inputs = uniqueSorted(append(slices.Clone(node.Inputs), seed.Outputs...))
		}
		originals[projectionKeys[node.ID]] = node
		node.ID = projectionKeys[node.ID]
		graph.Nodes = append(graph.Nodes, node)
	}
	for i := range graph.Artifacts {
		graph.Artifacts[i].Producer = projectionKeys[graph.Artifacts[i].Producer]
	}
	projected := topologicalFactoryNodes(graph)
	if len(projected) != len(ordered) {
		return nil, e.New(e.CodeEngineCapability, "shared source transport introduces a dependency cycle")
	}
	for i, node := range projected {
		projected[i] = originals[node.ID]
	}
	return projected, nil
}

func sourceRootForNode(plan *Plan, node Node, instruction ir.Node) (string, error) {
	_, artifacts := planIndex(plan)
	source := artifacts["source/"+node.BuildTarget+"/"+node.IRNode]
	suffix := "/" + instruction.Dest
	if source.Path == "" || !strings.HasSuffix(source.Path, suffix) {
		return "", e.New(e.CodePipelineDrift, "source output for %q does not bind its destination", node.ID)
	}
	return strings.TrimSuffix(source.Path, suffix), nil
}

// Only GitHub admits same-Run seeds; Tekton retains independent source fetching.
func githubSourceSeed(plan *Plan, nodeID string) (*pipeline.SourceSeed, error) {
	ordered, _, err := githubSchedulingProjection(plan)
	if err != nil {
		return nil, err
	}
	seeds, err := githubSourceSeeds(plan, ordered)
	if err != nil {
		return nil, err
	}
	seed, shared := seeds[nodeID]
	if !shared {
		return nil, nil
	}
	_, instruction, err := plan.producerInstruction(seed)
	if err != nil {
		return nil, err
	}
	root, err := sourceRootForNode(plan, seed, instruction)
	if err != nil {
		return nil, err
	}
	execution, err := plan.GitHubExecutionFor(seed)
	if err != nil {
		return nil, err
	}
	return &pipeline.SourceSeed{Instruction: instruction, Root: root, Platform: execution.Platform}, nil
}
