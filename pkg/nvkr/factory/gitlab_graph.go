// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const gitlabFinalizerJob = "nvkr:finalize"

// gitlabOperation is a provider projection, never an independently authored
// operation. Transfer membership remains complete even when scheduling reduces
// a transitively redundant prerequisite. Needs carries the shared execution
// graph's dependency result and Condition the typed scheduling condition that
// the renderer projects; neither is rederived from provider YAML.
type gitlabOperation struct {
	Node      Node
	Job       string
	Needs     []string
	Condition executionCondition
	Inputs    []Artifact
	Outputs   []Artifact
}

type gitlabGraph struct {
	Operations []gitlabOperation
	Jobs       map[string]string
}

func gitlabNodeJob(id string) string { return "nvkr:op:" + shortStableName(id) }
func gitlabBootstrapJob(platform string) string {
	return "nvkr:bootstrap:" + strings.ReplaceAll(platform, "/", "-")
}

func projectGitLabGraph(ctx context.Context, plan *Plan) (*gitlabGraph, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "GitLab projection requires a complete Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	nodes, artifacts := planIndex(plan)
	jobs := make(map[string]string, len(nodes))
	seen := map[string]bool{}
	for id := range nodes {
		job := gitlabNodeJob(id)
		if seen[job] {
			return nil, e.New(e.CodeEngineCapability, "GitLab operation job collision")
		}
		seen[job] = true
		jobs[id] = job
	}
	full := make(map[string][]string, len(nodes))
	for id, node := range nodes {
		for _, input := range node.Inputs {
			artifact, ok := artifacts[input]
			if !ok || jobs[artifact.Producer] == "" {
				return nil, e.New(e.CodeInvalidCatalog, "GitLab input %q has no exact producer", input)
			}
			full[jobs[id]] = append(full[jobs[id]], jobs[artifact.Producer])
		}
		full[jobs[id]] = uniqueSorted(full[jobs[id]])
	}
	operations, err := gitlabExecutionOperations(ctx, nodes, jobs, full)
	if err != nil {
		return nil, err
	}
	projection := &gitlabGraph{Jobs: jobs, Operations: operations}
	for i := range projection.Operations {
		operation := &projection.Operations[i]
		for _, input := range operation.Node.Inputs {
			operation.Inputs = append(operation.Inputs, artifacts[input])
		}
		for _, output := range operation.Node.Outputs {
			operation.Outputs = append(operation.Outputs, artifacts[output])
		}
	}
	return projection, nil
}

// gitlabExecutionOperations binds the complete raw projection to the shared
// execution graph owner. Each operation keeps its transitively reduced direct
// prerequisites and its node condition: ordinary operations schedule after
// success and verification operations after completion. The graph's
// dependencies result is what the renderer emits, so an ordinary descendant of
// a completion-scheduled verification node retains its original producers'
// success requirements instead of inheriting only the verification completion.
func gitlabExecutionOperations(ctx context.Context, nodes map[string]Node, jobs map[string]string, full map[string][]string) ([]gitlabOperation, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	reduced, err := reduceExecutionDependencies(ctx, full)
	if err != nil {
		return nil, err
	}
	operations := make(map[string]executionOperation, len(nodes))
	for _, id := range sortedKeys(nodes) {
		condition := executionAfterSuccess
		if nodes[id].Kind == NodeVerify {
			condition = executionAfterCompletion
		}
		operations[jobs[id]] = executionOperation{Needs: slices.Clone(reduced[jobs[id]]), Condition: condition}
	}
	graph, err := newExecutionGraph(ctx, operations)
	if err != nil {
		return nil, err
	}
	dependencies, err := graph.dependencies(ctx)
	if err != nil {
		return nil, err
	}
	projected := make([]gitlabOperation, 0, len(nodes))
	for _, id := range sortedKeys(nodes) {
		projected = append(projected, gitlabOperation{
			Node:      nodes[id],
			Job:       jobs[id],
			Needs:     slices.Clone(dependencies[jobs[id]]),
			Condition: operations[jobs[id]].Condition,
		})
	}
	return projected, nil
}

// GitLabJobNames is the complete reserved provider-name inventory. It describes
// expected membership; only the provider owner authenticates actual current jobs.
func GitLabJobNames(ctx context.Context, plan *Plan) ([]string, error) {
	projection, err := projectGitLabGraph(ctx, plan)
	if err != nil {
		return nil, err
	}
	names := []string{gitlabFinalizerJob}
	for _, operation := range projection.Operations {
		names = append(names, operation.Job)
	}
	if plan.Environment.GitLab != nil {
		platforms, err := gitlabUsedPlatforms(plan)
		if err != nil {
			return nil, err
		}
		for _, platform := range platforms {
			names = append(names, gitlabBootstrapJob(platform))
		}
	}
	return uniqueSorted(names), nil
}
