package factory

import (
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func validContractArtifactGraph() ArtifactGraph {
	return ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes: []Node{
			{ID: "build/a", Kind: NodeBuild, IRNode: "a", RunsOn: DefaultRunsOn, Container: "golang@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkNone, Outputs: []string{"artifact/a", "artifact/b"}},
			{ID: "build/b", Kind: NodeBuild, IRNode: "b", RunsOn: DefaultRunsOn, Container: "golang@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkNone, Inputs: []string{"artifact/a", "artifact/b"}, Outputs: []string{"artifact/c"}},
		},
		Artifacts: []Artifact{
			{ID: "artifact/a", Path: ".nkd/a", Producer: "build/a"},
			{ID: "artifact/b", Path: ".nkd/b", Producer: "build/a"},
			{ID: "artifact/c", Path: ".nkd/c", Producer: "build/b"},
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
		{"unknown network", func(g *ArtifactGraph) { g.Nodes[0].Network = "host" }, e.CodeInvalidCatalog, "network"},
		{"missing execution identity", func(g *ArtifactGraph) { g.Nodes[0].IRNode = "" }, e.CodeEngineCapability, "execution inputs"},
		{"unknown kind", func(g *ArtifactGraph) { g.Nodes[0].Kind = "unknown" }, e.CodeInvalidCatalog, "kind"},
		{"networked isolated node", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodePackage; g.Nodes[0].Network = ir.NetworkDefault }, e.CodeInvalidCatalog, "network none"},
		{"offline fetch", func(g *ArtifactGraph) { g.Nodes[0].Kind = NodeFetch }, e.CodeInvalidCatalog, "default network"},
		{"fetch without vendoring container", func(g *ArtifactGraph) {
			g.Nodes[0].Kind = NodeFetch
			g.Nodes[0].Network = ir.NetworkDefault
			g.Nodes[0].Container = ""
		}, e.CodeEngineCapability, "vendoring"},
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
