// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPlanRejectsChangedPackageAndProducerDataflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *Plan)
	}{
		{"package omits observation", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "package/worker")
			n.Inputs = slices.DeleteFunc(n.Inputs, func(id string) bool { return id == "observation/worker" })
		}},
		{"package includes selected tool as payload", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "package/worker")
			n.Inputs = uniqueSorted(append(n.Inputs, "tool/cosign"))
		}},
		{"build omits source closure", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "worker/build-containerd")
			n.Inputs = slices.DeleteFunc(n.Inputs, func(id string) bool { return strings.HasPrefix(id, "source/") })
		}},
		{"build omits source receipt", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "worker/build-containerd")
			n.Inputs = slices.DeleteFunc(n.Inputs, func(id string) bool { return strings.HasPrefix(id, "source-receipt/") })
		}},
		{"build introduces dependency", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "worker/build-containerd")
			n.Inputs = uniqueSorted(append(n.Inputs, "tool/cosign"))
		}},
		{"fetch introduces dependency", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, graphEdgeBuildInput(t, p, "source/").Producer)
			n.Inputs = []string{"tool/cosign"}
		}},
		{"observation omits producer result", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "worker/terminal-observe")
			n.Inputs = slices.DeleteFunc(n.Inputs, func(id string) bool { return id == "result/worker/containerd" })
		}},
		{"observation introduces dependency", func(t *testing.T, p *Plan) {
			n := graphEdgeNode(t, p, "worker/terminal-observe")
			n.Inputs = uniqueSorted(append(n.Inputs, "tool/cosign"))
		}},
		{"build adds output", func(t *testing.T, p *Plan) {
			a := *graphEdgeArtifact(t, p, "artifact/worker/containerd")
			a.ID, a.Name, a.Path = "artifact/worker/extra", "extra", ".nvkr/factory/worker/out/extra"
			n := graphEdgeNode(t, p, a.Producer)
			n.Outputs = uniqueSorted(append(n.Outputs, a.ID))
			p.Graph.Artifacts = append(p.Graph.Artifacts, a)
			slices.SortFunc(p.Graph.Artifacts, func(a, b Artifact) int { return strings.Compare(a.ID, b.ID) })
		}},
		{"package adds output", func(t *testing.T, p *Plan) {
			a := *graphEdgeArtifact(t, p, "package/worker")
			a.ID, a.Name, a.Path = "package/extra", "extra", ".nvkr/factory/packages/extra"
			n := graphEdgeNode(t, p, a.Producer)
			n.Outputs = uniqueSorted(append(n.Outputs, a.ID))
			p.Graph.Artifacts = append(p.Graph.Artifacts, a)
			slices.SortFunc(p.Graph.Artifacts, func(a, b Artifact) int { return strings.Compare(a.ID, b.ID) })
		}},
		{"source closure path", func(t *testing.T, p *Plan) {
			graphEdgeBuildInput(t, p, "source/").Path = ".nvkr/factory/worker/other-source"
		}},
		{"source receipt path", func(t *testing.T, p *Plan) {
			graphEdgeBuildInput(t, p, "source-receipt/").Path = ".nvkr/factory/worker/other-receipt.json"
		}},
		{"producer payload path", func(t *testing.T, p *Plan) {
			graphEdgeArtifact(t, p, "artifact/worker/containerd").Path = ".nvkr/factory/worker/other-payload"
		}},
		{"producer result path", func(t *testing.T, p *Plan) {
			graphEdgeArtifact(t, p, "result/worker/containerd").Path = ".nvkr/factory/worker/other-result.json"
		}},
		{"observation path", func(t *testing.T, p *Plan) {
			graphEdgeArtifact(t, p, "observation/worker").Path = ".nvkr/factory/worker/other-observation.json"
		}},
		{"package path", func(t *testing.T, p *Plan) {
			graphEdgeArtifact(t, p, "package/worker").Path = ".nvkr/factory/packages/other-worker"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
			if err := plan.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			tc.edit(t, plan)
			// These are well-formed DAGs. Only comparison with the admitted
			// Definition and producer IR can reject their altered meaning.
			if err := plan.Graph.Validate(); err != nil {
				t.Fatalf("mutation must retain a structurally valid graph: %v", err)
			}
			t.Run("validate", func(t *testing.T) {
				assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
			})
			t.Run("render", func(t *testing.T) {
				bundle, err := Render(t.Context(), plan, EngineGitHub)
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if bundle != nil {
					t.Fatal("invalid graph produced a generated bundle")
				}
			})
			t.Run("execute", func(t *testing.T) {
				workspace := t.TempDir()
				t.Chdir(workspace)
				var output bytes.Buffer
				err := ExecuteNode(t.Context(), plan, "package/worker", &output, &output)
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				entries, err := os.ReadDir(workspace)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 || output.Len() != 0 {
					t.Fatalf("invalid graph began execution: entries=%v output=%q", entries, output.String())
				}
			})
		})
	}
}

func graphEdgeNode(t *testing.T, plan *Plan, id string) *Node {
	t.Helper()
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].ID == id {
			return &plan.Graph.Nodes[i]
		}
	}
	t.Fatalf("fixture has no node %q", id)
	return nil
}

func graphEdgeArtifact(t *testing.T, plan *Plan, id string) *Artifact {
	t.Helper()
	for i := range plan.Graph.Artifacts {
		if plan.Graph.Artifacts[i].ID == id {
			return &plan.Graph.Artifacts[i]
		}
	}
	t.Fatalf("fixture has no artifact %q", id)
	return nil
}

func graphEdgeBuildInput(t *testing.T, plan *Plan, prefix string) *Artifact {
	t.Helper()
	for _, id := range graphEdgeNode(t, plan, "worker/build-containerd").Inputs {
		if strings.HasPrefix(id, prefix) {
			return graphEdgeArtifact(t, plan, id)
		}
	}
	t.Fatalf("fixture build has no input with prefix %q", prefix)
	return nil
}
