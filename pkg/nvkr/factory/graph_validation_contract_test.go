// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func validContractArtifactGraph() ArtifactGraph {
	return ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes: []Node{
			{ID: "build/a", Kind: NodePackage, RunsOn: DefaultRunsOn, Container: "golang@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkNone, Outputs: []string{"artifact/a", "artifact/b"}},
			{ID: "build/b", Kind: NodePackage, RunsOn: DefaultRunsOn, Container: "golang@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkNone, Inputs: []string{"artifact/a", "artifact/b"}, Outputs: []string{"artifact/c"}},
		},
		Artifacts: []Artifact{
			{ID: "artifact/a", Path: ".nvkr/a", Producer: "build/a"},
			{ID: "artifact/b", Path: ".nvkr/b", Producer: "build/a"},
			{ID: "artifact/c", Path: ".nvkr/c", Producer: "build/b"},
		},
	}
}

func TestArtifactGraphValidatesExecutionAndDataflowBindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*ArtifactGraph)
		code    e.Code
		message string
	}{
		{"shared producer inputs", func(*ArtifactGraph) {}, "", ""},
		{"duplicate node", func(g *ArtifactGraph) { g.Nodes[1].ID = g.Nodes[0].ID }, e.CodeInvalidCatalog, "repeated"},
		{"unsafe node ID", func(g *ArtifactGraph) { g.Nodes[0].ID = "build/../a" }, e.CodeInvalidCatalog, "invalid"},
		{"unsafe runner", func(g *ArtifactGraph) { g.Nodes[0].RunsOn = "ubuntu\ninjected" }, e.CodeInvalidCatalog, "runner"},
		{"unpinned container", func(g *ArtifactGraph) { g.Nodes[0].Container = "golang:latest" }, e.CodeUnpinnedSource, "unpinned"},
		{"wrong platform", func(g *ArtifactGraph) { g.Nodes[0].Platform = "darwin/arm64" }, e.CodeInvalidCatalog, "platform"},
		{"requirements on non-build node", func(g *ArtifactGraph) {
			g.Nodes[0].Kind = NodePackage
			g.Nodes[0].Requirements = &execution.Requirements{TimeoutSeconds: 1}
		}, e.CodeInvalidCatalog, "only on build"},
		{"unknown network", func(g *ArtifactGraph) { g.Nodes[0].Network = "host" }, e.CodeInvalidCatalog, "network"},
		{"missing execution identity", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodeBuild }, e.CodeEngineCapability, "producer reference"},
		{"unknown kind", func(g *ArtifactGraph) { g.Nodes[0].Kind = "unknown" }, e.CodeInvalidCatalog, "kind"},
		{"networked isolated node", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodePackage; g.Nodes[0].Network = ir.NetworkDefault }, e.CodeInvalidCatalog, "network none"},
		{"unbound fetch", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodeFetch }, e.CodeEngineCapability, "producer reference"},
		{"missing utility container", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodePackage; g.Nodes[0].Container = "" }, e.CodeEngineCapability, "utility"},
		{"no output", func(g *ArtifactGraph) { g.Nodes[0].Outputs = nil }, e.CodeInvalidCatalog, "no named outputs"},
		{"unsafe artifact path", func(g *ArtifactGraph) { g.Artifacts[0].Path = "../escape" }, e.CodeInvalidCatalog, "unsafe"},
		{"unknown producer", func(g *ArtifactGraph) { g.Artifacts[0].Producer = "build/missing" }, e.CodeInvalidCatalog, "unknown producer"},
		{"reversed artifacts", func(g *ArtifactGraph) { g.Artifacts[0], g.Artifacts[1] = g.Artifacts[1], g.Artifacts[0] }, e.CodeInvalidCatalog, "not canonical"},
		{"reversed inputs", func(g *ArtifactGraph) { g.Nodes[1].Inputs = []string{"artifact/b", "artifact/a"} }, e.CodeInvalidCatalog, "not canonical"},
		{"duplicate input", func(g *ArtifactGraph) { g.Nodes[1].Inputs = []string{"artifact/a", "artifact/a"} }, e.CodeInvalidCatalog, "repeats"},
		{"duplicate output", func(g *ArtifactGraph) { g.Nodes[1].Outputs = []string{"artifact/c", "artifact/c"} }, e.CodeInvalidCatalog, "repeats"},
		{"wrong producer binding", func(g *ArtifactGraph) { g.Artifacts[0].Producer = "build/b" }, e.CodeInvalidCatalog, "producer binding"},
		{"undeclared artifact", func(g *ArtifactGraph) { g.Nodes[0].Outputs = []string{"artifact/a"} }, e.CodeInvalidCatalog, "not declared"},
		{"missing input", func(g *ArtifactGraph) { g.Nodes[1].Inputs = []string{"artifact/missing"} }, e.CodeInvalidCatalog, "unknown artifact"},
		{"self input", func(g *ArtifactGraph) { g.Nodes[0].Inputs = []string{"artifact/a"} }, e.CodeInvalidCatalog, "own output"},
		{"cycle", func(g *ArtifactGraph) { g.Nodes[0].Inputs = []string{"artifact/c"} }, e.CodeInvalidCatalog, "dataflow cycle"},
		{"cycle with ready sibling", func(g *ArtifactGraph) {
			g.Nodes[0].Inputs = []string{"artifact/c"}
			sibling := g.Nodes[0]
			sibling.ID, sibling.Inputs, sibling.Outputs = "build/c", nil, []string{"artifact/d"}
			g.Nodes = append(g.Nodes, sibling)
			g.Artifacts = append(g.Artifacts, Artifact{ID: "artifact/d", Path: ".nvkr/d", Producer: "build/c"})
		}, e.CodeInvalidCatalog, "dataflow cycle"},
		{"missing input precedes cycle", func(g *ArtifactGraph) {
			g.Nodes[0].Inputs = []string{"artifact/c"}
			g.Nodes[1].Inputs = append(g.Nodes[1].Inputs, "artifact/missing")
		}, e.CodeInvalidCatalog, "unknown artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := validContractArtifactGraph()
			if err := graph.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			tc.edit(&graph)
			err := graph.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong rejection boundary: %v; want %q", err, tc.message)
			}
		})
	}
}

func TestArtifactGraphProducerReferenceRejectsCopiedExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Node)
	}{
		{"runner", func(node *Node) { node.RunsOn = DefaultRunsOn }},
		{"container", func(node *Node) { node.Container = "golang@sha256:" + strings.Repeat("a", 64) }},
		{"platform", func(node *Node) { node.Platform = "linux/amd64" }},
		{"network", func(node *Node) { node.Network = ir.NetworkNone }},
		{"requirements", func(node *Node) { node.Requirements = &execution.Requirements{TimeoutSeconds: 1} }},
		{"binding", func(node *Node) { node.ExecutionBinding = &execution.Binding{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := validContractArtifactGraph()
			graph.Nodes[0] = Node{ID: "build/a", Kind: NodeBuild, BuildTarget: "build", IRNode: "a", Outputs: []string{"artifact/a", "artifact/b"}}
			graph.Artifacts[0].BuildTarget = "build"
			graph.Artifacts[1].BuildTarget = "build"
			if err := graph.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.edit(&graph.Nodes[0])
			assertFactoryErrorCode(t, graph.Validate(), e.CodePipelineDrift)
		})
	}
}
