// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/url"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// githubSchedulingProjection gives provider scheduling a stable ordering without
// changing any executable Plan identity. Fetch IDs bind exact source pins; their
// provider keys instead describe the target and the components sharing that fetch.
// A pin update preserves this projection only while that consumer set stays equal.
func githubSchedulingProjection(plan *Plan) ([]Node, map[string]string, error) {
	if plan == nil {
		return nil, nil, e.New(e.CodeUsage, "GitHub scheduling requires a plan")
	}
	keys := make(map[string]string, len(plan.Graph.Nodes))
	originals := make(map[string]Node, len(plan.Graph.Nodes))
	projected := ArtifactGraph{Artifacts: slices.Clone(plan.Graph.Artifacts)}
	for _, node := range plan.Graph.Nodes {
		if _, duplicate := keys[node.ID]; duplicate {
			return nil, nil, e.New(e.CodeInvalidCatalog, "GitHub scheduling has duplicate node %q", node.ID)
		}
		key := node.ID
		if node.Kind == NodeFetch && node.IRNode != "" {
			var err error
			key, err = githubFetchSchedulingKey(plan, node)
			if err != nil {
				return nil, nil, err
			}
		}
		if previous, exists := originals[key]; exists {
			return nil, nil, e.New(e.CodeEngineCapability,
				"GitHub scheduling key %q collides for nodes %q and %q", key, previous.ID, node.ID)
		}
		keys[node.ID], originals[key] = key, node
		node.ID = key
		projected.Nodes = append(projected.Nodes, node)
	}
	for index := range projected.Artifacts {
		artifact := &projected.Artifacts[index]
		key, exists := keys[artifact.Producer]
		if !exists {
			return nil, nil, e.New(e.CodeInvalidCatalog,
				"GitHub artifact %q has no scheduling producer %q", artifact.ID, artifact.Producer)
		}
		artifact.Producer = key
	}
	ordered := topologicalFactoryNodes(projected)
	if len(ordered) != len(plan.Graph.Nodes) {
		return nil, nil, e.New(e.CodeEngineCapability, "GitHub scheduling projection has incomplete or cyclic dependencies")
	}
	for index, node := range ordered {
		ordered[index] = originals[node.ID]
	}
	return ordered, keys, nil
}

func githubFetchSchedulingKey(plan *Plan, node Node) (string, error) {
	var components []string
	found := false
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget || build.Graph == nil {
			continue
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.ID == node.IRNode && instruction.Kind == ir.KindFetch {
				found = true
			}
			if instruction.Kind == ir.KindBuild && slices.Contains(instruction.Needs, node.IRNode) {
				if instruction.Component == "" {
					return "", e.New(e.CodeEngineCapability, "GitHub fetch %q has an unnamed source consumer", node.ID)
				}
				components = append(components, instruction.Component)
			}
		}
	}
	if !found || node.BuildTarget == "" || len(components) == 0 {
		return "", e.New(e.CodeEngineCapability, "GitHub fetch %q lacks its exact source-consumer cohort", node.ID)
	}
	components = uniqueSorted(components)
	for index, component := range components {
		components[index] = url.PathEscape(component)
	}
	return node.BuildTarget + "/fetch/" + strings.Join(components, "/"), nil
}
