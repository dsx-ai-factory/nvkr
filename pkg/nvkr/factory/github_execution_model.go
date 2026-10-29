// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import "context"

// githubExecutionModel binds scheduling to typed stages before YAML rendering.
// Provider support operations share accounting but do not become build nodes.
func githubExecutionModel(ctx context.Context, plan *Plan, projection *githubNeedsProjection) (*executionGraph, map[string]bool, error) {
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, nil, err
	}
	operations := map[string]executionOperation{}
	inline := map[string]bool{}
	for id, parents := range projection.Reduced {
		if id == githubFinalizerJobID || projection.Collectors[id] != nil {
			continue
		}
		operations[id] = executionOperation{Needs: parents, Condition: executionAfterSuccess}
	}
	for _, stage := range stages {
		operation := operations[stage.ID]
		operation.Condition = stage.executionCondition()
		operations[stage.ID] = operation
		permissions, err := githubStagePermissions(plan, stage)
		if err != nil {
			return nil, nil, err
		}
		if permissions["id-token"] == "write" {
			inline[stage.ID] = true
		}
	}
	graph, err := newExecutionGraph(ctx, operations)
	return graph, inline, err
}

func (stage githubStage) executionCondition() executionCondition {
	if stage.DiskOutput != "" || githubProducerStage(stage) {
		return executionAfterSuccess
	}
	for _, node := range stage.Nodes {
		if node.Kind == NodeVerify {
			return executionAfterCompletion
		}
	}
	return executionAfterSuccess
}
