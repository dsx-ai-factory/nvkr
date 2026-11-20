// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func TestGitHubImagePublicationBatchBoundaries(t *testing.T) {
	for _, scenario := range []string{"compatible", "cycle", "full budget", "third route", "different runner", "different platform", "different credential", "disk", "image index", "aggregate"} {
		t.Run(scenario, func(t *testing.T) {
			plan := &Plan{Environment: definition.Environment{Publication: &definition.Publication{Mode: "per-output-v1"}},
				Targets:          map[string]Target{"output-a": {Kind: "oci", CredentialRef: "github:writer"}, "output-b": {Kind: "oci", CredentialRef: "github:writer"}},
				LifecycleOutputs: []LifecycleOutputPlan{{Name: "a", Kind: "oci"}, {Name: "b", Kind: "oci"}}}
			member := Node{ID: "attest/a", Kind: NodeAttestEvidence, Target: "output-a", RunsOn: "ubuntu-latest"}
			node := Node{ID: "attest/b", Kind: NodeAttestEvidence, Target: "output-b", RunsOn: "ubuntu-latest"}
			stages := []githubStage{{Nodes: []Node{member}}, {Nodes: []Node{{ID: "build/b", Kind: NodeBuild}}}}
			grouping := authorityStages{plan: plan, needs: map[int]map[int]bool{}}
			want := scenario == "compatible"
			switch scenario {
			case "cycle":
				grouping.needs[1] = map[int]bool{0: true}
			case "full budget":
				for len(stages[0].Nodes) < 6 {
					stages[0].Nodes = append(stages[0].Nodes, member)
				}
			case "third route":
				plan.Targets["output-c"] = plan.Targets["output-a"]
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, LifecycleOutputPlan{Name: "c", Kind: "oci"})
				other := member
				other.ID, other.Target = "attest/c", "output-c"
				stages[0].Nodes = append(stages[0].Nodes, other)
			case "different runner":
				node.RunsOn = "ubuntu-24.04-arm"
			case "different platform":
				node.Platform = "linux/arm64"
			case "different credential":
				plan.Targets["output-b"] = Target{Kind: "oci", CredentialRef: "github:other"}
			case "disk":
				plan.LifecycleOutputs[0].Kind = PackageDiskImage
			case "image index":
				plan.LifecycleOutputs[0].ImageIndex = &ImageIndexPlan{}
			case "aggregate":
				stages[0].Nodes[0].Kind = NodeAssembleRelease
			}
			index, joined := grouping.joinImagePublications(node, stages, []int{1})
			if joined != want || joined && index != 0 {
				t.Fatalf("join = %d/%t, want joined %t", index, joined, want)
			}
		})
	}
}

func TestGitHubImagePublicationBatchesPreserveOperations(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-dev/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	stages, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	grouping := authorityStages{plan: plan}
	imageJobs, routes, seen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, stage := range stages {
		budget, err := githubStageTimeoutMinutes(stage)
		if err != nil {
			t.Fatal(err)
		}
		if grouping.imagePublication(stage.Nodes[0]) {
			imageJobs[stage.ID] = true
			var total uint64
			targets := map[string]bool{}
			for _, node := range stage.Nodes {
				if !grouping.imagePublication(node) {
					t.Fatalf("image publication job includes %s", node.ID)
				}
				routes[node.Target] = true
				targets[node.Target] = true
				total += githubNodeTimeoutMinutes(node)
			}
			if len(targets) > 2 {
				t.Fatalf("job %s restores more than two output payloads", stage.ID)
			}
			if total != uint64(budget) || budget > githubMaximumJobTimeoutMinutes {
				t.Fatalf("job %s loses operation timeout reserve", stage.ID)
			}
		}
		for _, node := range stage.Nodes {
			if seen[node.ID] || jobs[node.ID] != stage.ID {
				t.Fatalf("lost or duplicated operation %s", node.ID)
			}
			seen[node.ID] = true
		}
	}
	if len(imageJobs) >= len(routes) || len(routes) < 2 {
		t.Fatalf("%d image jobs for %d image outputs: no batching", len(imageJobs), len(routes))
	}
	if len(seen) != len(plan.Graph.Nodes) {
		t.Fatal("lost graph operations")
	}
	// Scheduling must not rewrite any Plan input or artifact binding.
	after, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("scheduling changed the Plan")
	}
	t.Logf("image publication jobs: %d for %d outputs", len(imageJobs), len(routes))
}
