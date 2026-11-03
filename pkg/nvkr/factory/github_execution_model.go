// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import "context"

// Leave reusable-workflow headroom below the provider limit for later growth.
const githubTargetExecutionPartitions = 30

// githubExecutionModel binds scheduling to typed stages before YAML rendering.
// Provider support operations share accounting but do not become build nodes.
// Each operation's declared job timeout bounds its partition placement.
func githubExecutionModel(ctx context.Context, plan *Plan, projection *githubNeedsProjection) (*executionGraph, executionPartitioning, error) {
	layout := executionPartitioning{Bounds: map[string]int{}, Pinned: map[string]bool{},
		MaxJobs: githubMaximumResultInputs, Target: githubTargetExecutionPartitions}
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, layout, err
	}
	operations := map[string]executionOperation{}
	for id, parents := range projection.Reduced {
		if id == githubFinalizerJobID || projection.Collectors[id] != nil {
			continue
		}
		// Non-stage operations are the bounded runner bootstrap jobs.
		operations[id] = executionOperation{Needs: parents, Condition: executionAfterSuccess}
		layout.Bounds[id] = githubSupportJobTimeoutMinutes
	}
	for _, stage := range stages {
		operation := operations[stage.ID]
		operation.Condition = stage.executionCondition()
		operations[stage.ID] = operation
		if layout.Bounds[stage.ID], err = githubStageTimeoutMinutes(stage); err != nil {
			return nil, layout, err
		}
		permissions, err := githubStagePermissions(plan, stage)
		if err != nil {
			return nil, layout, err
		}
		if permissions["id-token"] == "write" {
			layout.Pinned[stage.ID] = true
		}
	}
	graph, err := newExecutionGraph(ctx, operations)
	return graph, layout, err
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
