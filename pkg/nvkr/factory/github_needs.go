// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const (
	githubFinalizerJobID      = "nvkr-lifecycle-run"
	githubResultBucketCount   = 16
	githubMaximumResultInputs = 64
)

// Full describes the original provider dependencies. Reduced describes every
// emitted job, including result collectors. Collectors partition the original
// finalizer dependencies; they never replace a producer's result or execution.
type githubNeedsProjection struct {
	Full       map[string][]string
	Reduced    map[string][]string
	Collectors map[string][]string
}

var githubNeedsWord = regexp.MustCompile(`(?i)\bneeds\b`)

// The calling execution/generation boundary admits the complete Plan. This
// projection independently validates its provider DAG and performs no I/O.
func githubNeedsForPlan(ctx context.Context, plan *Plan) (*githubNeedsProjection, error) {
	if err := githubNeedsContext(ctx); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "GitHub dependency projection requires a Plan")
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return nil, err
	}
	full := map[string][]string{}
	bootstrap := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		resolved, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, err
		}
		id := "build-runner-" + shortStableName(resolved.RunsOn)
		bootstrap[resolved.RunsOn] = id
		full[id] = nil
	}
	_, artifacts := planIndex(plan)
	for _, stage := range stages {
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		if _, exists := full[stage.ID]; exists {
			return nil, e.New(e.CodeEngineCapability, "GitHub stage %q collides with a bootstrap job", stage.ID)
		}
		full[stage.ID] = stage.needs(artifacts, nodeJobs, bootstrap[stage.Nodes[0].RunsOn])
	}
	if plan.DefinitionDigest != "" && plan.EnvironmentDigest != "" {
		full[githubFinalizerJobID], err = githubFinalizerJobs(plan, nodeJobs)
		if err != nil {
			return nil, err
		}
	}
	// Authored setup, environment, and container values can observe the direct needs context.
	// Preserve all ordinary lists when any such value might use that context.
	retain := githubNeedsWord.MatchString(plan.Environment.Runtime.RuntimeImage)
	for _, value := range plan.Environment.Runtime.Setup {
		retain = retain || githubNeedsWord.MatchString(value)
	}
	for _, value := range plan.Environment.Runtime.Variables {
		retain = retain || githubNeedsWord.MatchString(value)
	}
	for _, value := range plan.Environment.Runtime.Services {
		retain = retain || githubNeedsWord.MatchString(value)
	}
	return projectGitHubNeeds(ctx, full, retain)
}

func projectGitHubNeeds(ctx context.Context, full map[string][]string, retainOrdinary bool) (*githubNeedsProjection, error) {
	reduced, err := reduceGitHubNeeds(ctx, full)
	if err != nil {
		return nil, err
	}
	projection := &githubNeedsProjection{Full: map[string][]string{}, Reduced: reduced, Collectors: map[string][]string{}}
	for _, id := range sortedKeys(full) {
		projection.Full[id] = uniqueSorted(full[id])
		if retainOrdinary || id == githubFinalizerJobID {
			projection.Reduced[id] = slices.Clone(projection.Full[id])
		}
	}
	leaves := projection.Full[githubFinalizerJobID]
	if len(leaves) <= githubMaximumResultInputs {
		return projection, githubNeedsContext(ctx)
	}
	for _, leaf := range leaves {
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		id := githubResultCollectorID(leaf)
		if _, exists := full[id]; exists {
			return nil, e.New(e.CodeEngineCapability, "GitHub result collector %q collides with an original job", id)
		}
		projection.Collectors[id] = append(projection.Collectors[id], leaf)
		if len(projection.Collectors[id]) > githubMaximumResultInputs {
			return nil, e.New(e.CodeEngineCapability, "GitHub result collector %q exceeds %d dependencies", id, githubMaximumResultInputs)
		}
	}
	for _, id := range sortedKeys(projection.Collectors) {
		projection.Reduced[id] = slices.Clone(projection.Collectors[id])
	}
	projection.Reduced[githubFinalizerJobID] = sortedKeys(projection.Collectors)
	return projection, githubNeedsContext(ctx)
}

// FNV is a fixed routing function, not an artifact or authority digest. Fixed
// buckets keep existing membership unchanged when independent outputs are added.
func githubResultCollectorID(job string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(job))
	return fmt.Sprintf("nvkr-results-%02x", hash.Sum64()%githubResultBucketCount)
}

// A DAG has a unique transitive reduction. Compute ancestor sets once, then
// remove only an edge already supplied by another direct dependency. Conditions,
// execution bodies and all original ancestor relationships remain unchanged.
func reduceGitHubNeeds(ctx context.Context, full map[string][]string) (map[string][]string, error) {
	if err := githubNeedsContext(ctx); err != nil {
		return nil, err
	}
	ordered := sortedKeys(full)
	dependencies := make(map[string][]string, len(full))
	for _, id := range ordered {
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		if id == "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub dependency graph has an empty job")
		}
		dependencies[id] = uniqueSorted(full[id])
		if len(dependencies[id]) != len(full[id]) {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub job %q repeats a dependency", id)
		}
		for _, parent := range dependencies[id] {
			if _, exists := full[parent]; !exists || parent == id {
				return nil, e.New(e.CodeInvalidCatalog, "GitHub job %q has an unknown or self dependency", id)
			}
		}
	}
	ancestors := make(map[string]map[string]bool, len(full))
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if err := githubNeedsContext(ctx); err != nil {
			return err
		}
		if _, done := ancestors[id]; done {
			return nil
		}
		if visiting[id] {
			return e.New(e.CodeInvalidCatalog, "GitHub dependency graph contains a cycle")
		}
		visiting[id] = true
		parents := map[string]bool{}
		for _, parent := range dependencies[id] {
			if err := visit(parent); err != nil {
				return err
			}
			parents[parent] = true
			for ancestor := range ancestors[parent] {
				if err := githubNeedsContext(ctx); err != nil {
					return err
				}
				parents[ancestor] = true
			}
		}
		delete(visiting, id)
		ancestors[id] = parents
		return nil
	}
	for _, id := range ordered {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	reduced := make(map[string][]string, len(full))
	for _, id := range ordered {
		reduced[id] = nil
		for _, parent := range dependencies[id] {
			if err := githubNeedsContext(ctx); err != nil {
				return nil, err
			}
			redundant := false
			for _, other := range dependencies[id] {
				if other != parent && ancestors[other][parent] {
					redundant = true
					break
				}
			}
			if !redundant {
				reduced[id] = append(reduced[id], parent)
			}
		}
	}
	return reduced, githubNeedsContext(ctx)
}

func githubNeedsContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitHub dependency projection requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub dependency projection canceled")
	}
	return nil
}

func githubResultCollector(plan *Plan, id string, members []string) githubJob {
	return githubJob{
		Name: "Collect provider results: " + id, RunsOn: "ubuntu-24.04", TimeoutMinutes: 5,
		Needs: slices.Clone(members), If: "${{ always() && " + githubDispatchCondition(plan) + " }}",
		Permissions: map[string]string{},
		Outputs:     map[string]string{"results": "${{ toJSON(needs) }}", "statuses": "${{ join(needs.*.result, ' ') }}"},
		Steps:       []githubStep{{Name: "Forward provider results", Run: ":"}},
	}
}

// Lower only an exact original job/dependency set. Retain aliases and comments:
// resolving the shared step anchors here would undo the earlier body compaction.
func lowerGitHubNeeds(ctx context.Context, plan *Plan, raw []byte) ([]byte, error) {
	projection, err := githubNeedsForPlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, e.New(e.CodePipelineDrift, "GitHub dependency lowering requires one valid workflow")
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if !githubYAMLOnlyFields(jobs, sortedKeys(projection.Full)...) || githubYAMLLength(jobs) != 2*len(projection.Full) {
		return nil, e.New(e.CodePipelineDrift, "GitHub dependency lowering requires the exact original jobs")
	}
	for index := 0; index < len(jobs.Content); index += 2 {
		id, job := jobs.Content[index].Value, jobs.Content[index+1]
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		if job.Kind != yamlv3.MappingNode {
			return nil, e.New(e.CodePipelineDrift, "GitHub job %q requires an original job mapping", id)
		}
		original, err := githubNeedsValues(githubYAMLField(job, "needs"))
		if err != nil || !slices.Equal(uniqueSorted(original), projection.Full[id]) {
			return nil, e.New(e.CodePipelineDrift, "GitHub job %q changes its original dependencies", id)
		}
		if len(projection.Reduced[id]) != 0 {
			githubYAMLSet(job, "needs", githubNeedsSequence(projection.Reduced[id]))
		}
	}
	if len(projection.Collectors) != 0 {
		content := make([]*yamlv3.Node, 0, len(jobs.Content)+2*len(projection.Collectors))
		for index := 0; index < len(jobs.Content); index += 2 {
			if jobs.Content[index].Value == githubFinalizerJobID {
				for _, id := range sortedKeys(projection.Collectors) {
					content = append(content, githubYAMLString(id), githubResultCollectorYAML(plan, id, projection.Collectors[id]))
				}
			}
			content = append(content, jobs.Content[index], jobs.Content[index+1])
		}
		jobs.Content = content
	}
	if err := githubNeedsContext(ctx); err != nil {
		return nil, err
	}
	return encodeGitHubYAML(&document)
}

func githubNeedsValues(node *yamlv3.Node) ([]string, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yamlv3.SequenceNode {
		return nil, e.New(e.CodePipelineDrift, "GitHub dependencies require an exact sequence")
	}
	values := make([]string, 0, len(node.Content))
	seen := map[string]bool{}
	for _, value := range node.Content {
		if value.Kind != yamlv3.ScalarNode || value.Tag != "!!str" || value.Value == "" || seen[value.Value] {
			return nil, e.New(e.CodePipelineDrift, "GitHub dependencies contain an invalid or duplicate job")
		}
		seen[value.Value] = true
		values = append(values, value.Value)
	}
	return values, nil
}

func githubNeedsSequence(values []string) *yamlv3.Node {
	sequence := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Style: yamlv3.FlowStyle}
	for _, value := range values {
		sequence.Content = append(sequence.Content, githubYAMLString(value))
	}
	return sequence
}

func githubResultCollectorYAML(plan *Plan, id string, members []string) *yamlv3.Node {
	job := githubResultCollector(plan, id, members)
	return githubYAMLMap("name", githubYAMLString(job.Name), "runs-on", githubYAMLString(job.RunsOn),
		"timeout-minutes", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!int", Value: "5"},
		"needs", githubNeedsSequence(job.Needs), "if", githubYAMLString(job.If), "permissions", githubYAMLMap(),
		"outputs", githubYAMLMap("results", githubYAMLString(job.Outputs["results"]), "statuses", githubYAMLString(job.Outputs["statuses"])),
		"steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: []*yamlv3.Node{
			githubYAMLMap("name", githubYAMLString(job.Steps[0].Name), "run", githubYAMLString(job.Steps[0].Run)),
		}})
}
