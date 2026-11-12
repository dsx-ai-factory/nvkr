// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// tektonProjection is descriptive scheduling over the same grouped operations
// as GitHub. It contains no producer commands or alternative acceptance policy.
type tektonProjection struct {
	stages       []githubStage
	owners       map[string]string
	dependencies map[string][]string
}

func projectTektonExecution(ctx context.Context, plan *Plan, platform string) (*tektonProjection, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "Tekton projection requires a complete Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if (platform != "linux/amd64" && platform != "linux/arm64") || plan.Environment.UtilityPlatform != platform {
		return nil, e.New(e.CodeEngineCapability, "Tekton requires one explicit native Linux platform matching the utility platform")
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == PackageDiskImage {
			return nil, e.New(e.CodeEngineCapability, "Tekton does not admit OS image outputs")
		}
	}
	for _, build := range plan.Builds {
		if build.Platform != platform {
			return nil, e.New(e.CodeEngineCapability, "Tekton build %q requires %s; admitted native platform is %s", build.Name, build.Platform, platform)
		}
	}
	placement := func(node Node) (Node, error) {
		resolved, err := plan.ExecutionFor(node)
		if err != nil {
			return Node{}, err
		}
		if resolved.ExecutionBinding != nil || resolved.Requirements != nil ||
			(resolved.Platform != "" && resolved.Platform != platform) {
			return Node{}, e.New(e.CodeEngineCapability, "Tekton operation %q requires an unsupported execution capability or platform", node.ID)
		}
		// Runner labels are provider placement, not logical build identity.
		// One native pod pool replaces GitHub's placement after resolution.
		resolved.RunsOn = "tekton-native"
		return resolved, nil
	}
	stages, owners, err := projectExecutionStages(plan, placement)
	if err != nil {
		return nil, err
	}
	_, artifacts := planIndex(plan)
	operations := make(map[string]executionOperation, len(stages))
	for _, stage := range stages {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		var needs []string
		for _, input := range stage.inputs(artifacts, owners) {
			needs = append(needs, owners[artifacts[input].Producer])
		}
		operations[stage.ID] = executionOperation{Needs: uniqueSorted(needs), Condition: stage.executionCondition()}
	}
	graph, err := newExecutionGraph(ctx, operations)
	if err != nil {
		return nil, err
	}
	dependencies, err := graph.dependencies(ctx)
	if err != nil {
		return nil, err
	}
	return &tektonProjection{stages: stages, owners: owners, dependencies: dependencies}, nil
}
