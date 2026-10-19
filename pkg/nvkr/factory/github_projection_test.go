// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These descriptions deliberately stop short of Plan admission so the provider
// projection's own rejection paths are exercised, rather than Plan.Validate.
func githubSchedulingFixture() *Plan {
	return &Plan{
		Builds: []BuildPlan{{Name: "target", Graph: &ir.Graph{Nodes: []ir.Node{
			{ID: "fetch-pinned", Kind: ir.KindFetch},
			{ID: "build-worker", Kind: ir.KindBuild, Component: "worker", Needs: []string{"fetch-pinned"}},
		}}}},
		Graph: ArtifactGraph{
			Nodes: []Node{
				{ID: "ready", Kind: NodeBuild},
				{ID: "target/fetch-pinned", Kind: NodeFetch, BuildTarget: "target", IRNode: "fetch-pinned"},
				{ID: "worker", Kind: NodeBuild, Inputs: []string{"source"}},
			},
			Artifacts: []Artifact{{ID: "source", Producer: "target/fetch-pinned"}},
		},
	}
}

func TestGitHubSchedulingProjectionRejectsInvalidGraphWithoutPartialOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*Plan)
		code       e.Code
		diagnostic string
	}{
		{name: "duplicate node", mutate: func(plan *Plan) {
			plan.Graph.Nodes = append(plan.Graph.Nodes, plan.Graph.Nodes[0])
		}, code: e.CodeInvalidCatalog, diagnostic: "duplicate node"},
		{name: "two fetches share one scheduling key", mutate: func(plan *Plan) {
			plan.Builds[0].Graph.Nodes = append(plan.Builds[0].Graph.Nodes,
				ir.Node{ID: "fetch-other-pin", Kind: ir.KindFetch},
				ir.Node{ID: "build-other", Kind: ir.KindBuild, Component: "worker", Needs: []string{"fetch-other-pin"}})
			plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: "target/fetch-other-pin", Kind: NodeFetch, BuildTarget: "target", IRNode: "fetch-other-pin"})
		}, code: e.CodeEngineCapability, diagnostic: "scheduling key"},
		{name: "fetch key collides with ordinary node", mutate: func(plan *Plan) {
			plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: "target/fetch/worker", Kind: NodeBuild})
		}, code: e.CodeEngineCapability, diagnostic: "scheduling key"},
		{name: "missing build target", mutate: func(plan *Plan) {
			plan.Graph.Nodes[1].BuildTarget = "absent"
		}, code: e.CodeEngineCapability, diagnostic: "source-consumer cohort"},
		{name: "missing fetch instruction", mutate: func(plan *Plan) {
			plan.Builds[0].Graph.Nodes = plan.Builds[0].Graph.Nodes[1:]
		}, code: e.CodeEngineCapability, diagnostic: "source-consumer cohort"},
		{name: "fetch has no actual consumer", mutate: func(plan *Plan) {
			plan.Builds[0].Graph.Nodes[1].Needs = nil
		}, code: e.CodeEngineCapability, diagnostic: "source-consumer cohort"},
		{name: "unnamed source consumer", mutate: func(plan *Plan) {
			plan.Builds[0].Graph.Nodes[1].Component = ""
		}, code: e.CodeEngineCapability, diagnostic: "unnamed source consumer"},
		{name: "artifact has no producer", mutate: func(plan *Plan) {
			plan.Graph.Artifacts[0].Producer = "absent"
		}, code: e.CodeInvalidCatalog, diagnostic: "no scheduling producer"},
		{name: "dependency has no artifact", mutate: func(plan *Plan) {
			plan.Graph.Nodes[2].Inputs = []string{"absent"}
		}, code: e.CodeEngineCapability, diagnostic: "incomplete or cyclic dependencies"},
		{name: "cycle after independent ready node", mutate: func(plan *Plan) {
			plan.Graph.Nodes[1].Inputs = []string{"result"}
			plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: "result", Producer: "worker"})
		}, code: e.CodeEngineCapability, diagnostic: "incomplete or cyclic dependencies"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubSchedulingFixture()
			test.mutate(plan)
			ordered, keys, err := githubSchedulingProjection(plan)
			assertFactoryErrorCode(t, err, test.code)
			if !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("projection reached the wrong guard: %v", err)
			}
			if ordered != nil || keys != nil {
				t.Fatalf("rejected projection leaked partial nodes or keys: %v / %v", ordered, keys)
			}
			stages, jobs, err := githubStages(plan)
			assertFactoryErrorCode(t, err, test.code)
			if stages != nil || jobs != nil {
				t.Fatalf("rejected projection emitted stages or provider jobs: %v / %v", stages, jobs)
			}
		})
	}
}

func TestGitHubStagesRejectProviderJobIDCollisionsWithoutPartialOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
	}{
		{"normalized job IDs", []string{"build/worker", "build-worker"}},
		{"reserved finalizer ID", []string{"ready", "lifecycle/run"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := &Plan{}
			for _, id := range test.ids {
				plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: id, Kind: NodeBuild})
			}
			ordered, keys, err := githubSchedulingProjection(plan)
			if err != nil || len(ordered) != len(test.ids) || len(keys) != len(test.ids) {
				t.Fatalf("fixture must pass scheduling-key admission before provider normalization: %v", err)
			}
			stages, jobs, err := githubStages(plan)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), "GitHub job ID") || !strings.Contains(err.Error(), "collides") {
				t.Fatalf("collision reached the wrong guard: %v", err)
			}
			if stages != nil || jobs != nil {
				t.Fatalf("collision emitted partial stages or jobs: %v / %v", stages, jobs)
			}
		})
	}
}
