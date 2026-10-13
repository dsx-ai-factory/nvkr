// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

// NewProgress projects the exact validated graph into descriptive monitor rows.
// It performs no execution or I/O and admits no capabilities. Its immutable row
// count follows the admitted graph, never a UI page or activity-ring limit.
func NewProgress(plan *Plan, attempt string) (*progress.Monitor, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "progress requires a plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	return progress.New(projectProgress(plan, attempt, digest)), nil
}

func projectProgress(plan *Plan, attempt, digest string) progress.Snapshot {
	snapshot := progress.Snapshot{Attempt: attempt, PlanDigest: digest, Mode: "inspection", Phase: "plan",
		Lane: plan.Qualification.String(), Status: "pending"}
	nodes, artifacts := planIndex(plan)
	// Native targets are exact compiler-owned references, including index children.
	targetOutput := make(map[string]string)
	for _, output := range plan.LifecycleOutputs {
		if output.ImageIndex == nil {
			targetOutput[output.Name] = output.Name
		} else {
			for _, child := range output.ImageIndex.Children {
				targetOutput[child.BuildTarget] = output.Name
			}
		}
	}
	membership := make(map[string]map[string]bool, len(nodes))
	parents := make(map[string][]string, len(nodes))
	for _, node := range plan.Graph.Nodes {
		for _, input := range node.Inputs {
			parents[node.ID] = append(parents[node.ID], artifacts[input].Producer)
		}
		parents[node.ID] = uniqueSorted(parents[node.ID])
		membership[node.ID] = make(map[string]bool)
	}
	for _, output := range plan.LifecycleOutputs {
		row := progress.Output{Name: output.Name, Kind: output.Kind, State: "required"}
		if output.ImageIndex != nil {
			row.Platform = "aggregate"
		} else {
			for _, authored := range plan.Definition.Outputs {
				if authored.Name == output.Name {
					row.Platform = authored.Platform
					break
				}
			}
		}
		// Include every selected native operation and the exact final payload owner;
		// follow typed artifact edges to retain transitive/shared prerequisites.
		var pending []string
		if artifact, ok := artifacts[output.ArtifactID]; ok {
			pending = append(pending, artifact.Producer)
		}
		for _, node := range plan.Graph.Nodes {
			if targetOutput[node.BuildTarget] == output.Name {
				pending = append(pending, node.ID)
			}
		}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if membership[id][output.Name] {
				continue
			}
			membership[id][output.Name] = true
			pending = append(pending, parents[id]...)
			if other := targetOutput[nodes[id].BuildTarget]; other != "" && other != output.Name {
				row.Dependencies = append(row.Dependencies, other)
			}
		}
		row.Dependencies = uniqueSorted(row.Dependencies)
		snapshot.Outputs = append(snapshot.Outputs, row)
	}
	// Downstream publication/finalization nodes inherit exact input membership.
	// Do not propagate their union backwards across independent sibling outputs.
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		if len(membership[node.ID]) != 0 {
			continue
		}
		for _, parent := range parents[node.ID] {
			toolOnly := len(nodes[parent].Outputs) != 0
			for _, output := range nodes[parent].Outputs {
				if artifacts[output].Kind != "pipeline-tool" {
					toolOnly = false
					break
				}
			}
			if toolOnly {
				continue
			}
			for name := range membership[parent] {
				membership[node.ID][name] = true
			}
		}
	}
	for _, node := range plan.Graph.Nodes {
		row := progress.Operation{ID: node.ID, Kind: node.Kind, State: "queued", Dependencies: slices.Clone(parents[node.ID])}
		row.Outputs = sortedKeys(membership[node.ID])
		snapshot.Operations = append(snapshot.Operations, row)
	}
	slices.SortFunc(snapshot.Operations, func(a, b progress.Operation) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for index := range snapshot.Outputs {
		for _, operation := range snapshot.Operations {
			if slices.Contains(operation.Outputs, snapshot.Outputs[index].Name) {
				snapshot.Outputs[index].Operations = append(snapshot.Outputs[index].Operations, operation.ID)
			}
		}
	}
	return snapshot
}
