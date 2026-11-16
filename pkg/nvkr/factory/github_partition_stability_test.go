// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yamlv3 "go.yaml.in/yaml/v3"
)

// Exercise the maintained Plan's real jobs and worker bytes, including exports
// to other workers and collectors. A new preflight ahead of every root must not
// renumber existing workers or spend a split budget belonging to another depth.
func TestGitHubPartitionRootAdditionChurn(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	model, layout, err := githubExecutionModel(t.Context(), plan, projection)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := githubFlatReference(t, plan, bundle).Files[bundle.WorkflowPath]
	before := renderPartitionChurnFixture(t, plan, raw, model, layout, projection)
	operations := maps.Clone(model.operations)
	const root = "new-preflight"
	for id, operation := range operations {
		if len(operation.Needs) == 0 {
			operation.Needs = []string{root}
			operations[id] = operation
		}
	}
	operations[root] = executionOperation{Condition: executionAfterSuccess}
	changed, err := newExecutionGraph(t.Context(), operations)
	if err != nil {
		t.Fatal(err)
	}
	layout.Bounds[root] = githubSupportJobTimeoutMinutes
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	githubYAMLSet(jobs, root, githubYAMLMap("name", githubYAMLString("New preflight"),
		"if", githubYAMLString("${{ "+githubDispatchCondition(plan)+" }}"),
		"runs-on", githubYAMLString("ubuntu-24.04"), "steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Content: []*yamlv3.Node{githubYAMLMap("run", githubYAMLString(":"))}}))
	for id, operation := range operations {
		if slices.Equal(operation.Needs, []string{root}) {
			githubYAMLSet(githubYAMLField(jobs, id), "needs", githubNeedsSequence(operation.Needs))
		}
	}
	raw, err = encodeGitHubYAML(document.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	after := renderPartitionChurnFixture(t, plan, raw, changed, layout, projection)
	modified, removed, added := 0, 0, 0
	for path, original := range before {
		if current, ok := after[path]; !ok {
			removed++
		} else if !bytes.Equal(current, original) {
			modified++
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			added++
		}
	}
	t.Logf("workers: before=%d after=%d modified=%d removed=%d added=%d", len(before), len(after), modified, removed, added)
	if len(before) < 30 || modified+removed+added > 2 || added != 1 {
		t.Fatal("adding one root changed more than two worker files or failed to add its worker")
	}
}

func renderPartitionChurnFixture(t *testing.T, plan *Plan, raw []byte, model *executionGraph, layout executionPartitioning, projection *githubNeedsProjection) map[string][]byte {
	t.Helper()
	dependencies, err := model.dependencies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	partitions, err := partitionGitHubJobs(t.Context(), dependencies, layout)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for id := range layout.Pinned {
		owners[id] = id
	}
	var consumers []githubPartitionExport
	for _, partition := range partitions {
		id := githubPartitionID(partition)
		var parents []string
		for _, member := range partition.Jobs {
			owners[member] = id
			parents = append(parents, dependencies[member]...)
		}
		consumers = append(consumers, githubPartitionExport{Name: id, Members: uniqueSorted(parents)})
	}
	for _, id := range sortedKeys(projection.Collectors) {
		consumers = append(consumers, githubPartitionExport{Name: id, Members: projection.Collectors[id], Statuses: true})
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	files := map[string][]byte{}
	for _, partition := range partitions {
		id := githubPartitionID(partition)
		path := strings.TrimSuffix(githubLayoutFor(plan).workflowPath, ".yml") + "__" + id + ".yml"
		if len(strings.TrimSuffix(strings.Split(filepath.Base(path), "__")[1], ".yml")) > 64 {
			t.Fatalf("worker exceeds template limit: %s", path)
		}
		files[path], err = renderGitHubPartition(t.Context(), plan, raw, partition, model,
			githubPartitionExports(id, consumers, owners), githubPartitionName(partition, jobs))
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}
