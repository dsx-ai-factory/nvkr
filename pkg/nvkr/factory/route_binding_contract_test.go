// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// A valid artifact DAG does not authorize publication: every executable route
// and its signing edges must still match the admitted Definition and Environment.
func TestPlanRejectsPublicationRouteGraphDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signed bool
		edit   func(*testing.T, *Plan)
	}{
		{"extra unsigned publisher", false, addUnboundRoutePublisher},
		{"extra publisher sharing signed target", true, addUnboundRoutePublisher},
		{"substituted package", false, func(t *testing.T, p *Plan) {
			routeContractNode(t, p, "publish/release").Inputs = []string{"package/worker"}
		}},
		{"extra package input", false, func(t *testing.T, p *Plan) {
			routeContractNode(t, p, "publish/release").Inputs = []string{"package/release", "package/worker"}
		}},
		{"substituted target", false, func(t *testing.T, p *Plan) {
			routeContractNode(t, p, "publish/release").Target = "other"
			routeContractArtifact(t, p, "publication/release").Target = "other"
		}},
		{"renamed publication", false, func(t *testing.T, p *Plan) {
			routeContractNode(t, p, "publish/release").Outputs = []string{"publication/other"}
			routeContractArtifact(t, p, "publication/release").ID = "publication/other"
		}},
		{"publication path", false, func(t *testing.T, p *Plan) {
			routeContractArtifact(t, p, "publication/release").Path = ".nvkr/factory/publications/other.txt"
		}},
		{"publication target", false, func(t *testing.T, p *Plan) {
			routeContractArtifact(t, p, "publication/release").Target = "other"
		}},
		{"signing different package", true, func(t *testing.T, p *Plan) {
			node := routeContractNode(t, p, "sign/release")
			i := slices.Index(node.Inputs, "package/release")
			node.Inputs[i] = "package/worker"
		}},
		{"signing without publication", true, func(t *testing.T, p *Plan) {
			node := routeContractNode(t, p, "sign/release")
			node.Inputs = slices.DeleteFunc(node.Inputs, func(id string) bool { return id == "publication/release" })
		}},
		{"verification without signature", true, func(t *testing.T, p *Plan) {
			node := routeContractNode(t, p, "verify-publication/release")
			node.Inputs = slices.DeleteFunc(node.Inputs, func(id string) bool { return id == "signature/release" })
		}},
		{"verification without publication", true, func(t *testing.T, p *Plan) {
			node := routeContractNode(t, p, "verify-publication/release")
			node.Inputs = slices.DeleteFunc(node.Inputs, func(id string) bool { return id == "publication/release" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			if tc.signed {
				plan = signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
			}
			if err := plan.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			tc.edit(t, plan)
			slices.SortFunc(plan.Graph.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
			slices.SortFunc(plan.Graph.Artifacts, func(a, b Artifact) int { return strings.Compare(a.ID, b.ID) })
			if err := plan.Graph.Validate(); err != nil {
				t.Fatalf("mutation must retain a structurally valid graph: %v", err)
			}
			t.Run("validate", func(t *testing.T) {
				assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
			})
			t.Run("render", func(t *testing.T) {
				bundle, err := RenderGitHub(t.Context(), plan)
				if bundle != nil {
					t.Error("unauthorized publication graph emitted a workflow bundle")
				}
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			})
		})
	}
}

func addUnboundRoutePublisher(t *testing.T, plan *Plan) {
	t.Helper()
	node := *routeContractNode(t, plan, "publish/release")
	artifact := *routeContractArtifact(t, plan, "publication/release")
	node.ID = "publish/aaa"
	node.Inputs = []string{"package/worker"}
	node.Outputs = []string{"publication/aaa"}
	artifact.ID, artifact.Name, artifact.Producer = "publication/aaa", "aaa", node.ID
	artifact.Path = ".nvkr/factory/publications/aaa.txt"
	plan.Graph.Nodes = append(plan.Graph.Nodes, node)
	plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
}

func routeContractNode(t *testing.T, plan *Plan, id string) *Node {
	t.Helper()
	i := slices.IndexFunc(plan.Graph.Nodes, func(node Node) bool { return node.ID == id })
	if i < 0 {
		t.Fatalf("fixture omitted node %q", id)
	}
	return &plan.Graph.Nodes[i]
}

func routeContractArtifact(t *testing.T, plan *Plan, id string) *Artifact {
	t.Helper()
	i := slices.IndexFunc(plan.Graph.Artifacts, func(artifact Artifact) bool { return artifact.ID == id })
	if i < 0 {
		t.Fatalf("fixture omitted artifact %q", id)
	}
	return &plan.Graph.Artifacts[i]
}
