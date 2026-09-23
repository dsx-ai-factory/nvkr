package ir

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func ordinaryMaterialGraph() Graph {
	graph := sample()
	graph.Nodes[0].Needs = []string{"fetch-kubernetes-abc", "pull-runtime-archive"}
	graph.Nodes[0].Materials = []MaterialBinding{{
		Binding: "runtime-archive", Component: "runtime-archive", Producer: "pull-runtime-archive",
	}}
	graph.Nodes = append(graph.Nodes[:2], append([]Node{{
		ID: "pull-runtime-archive", Kind: KindPull, Component: "runtime-archive",
		Artifact: "https://example.test/runtime.tar.gz", Digest: "sha256:" + strings.Repeat("e", 64),
		Platform: "linux/amd64", Network: NetworkDefault, OutputType: "tarball",
		ArtifactPath: "runtime.tar.gz", Version: "1.0.0",
	}}, graph.Nodes[2:]...)...)
	graph.Nodes[3].Needs = []string{"build-kubelet", "pull-runtime-archive"}
	return graph
}

func TestOCIHermeticMaterialGraphBindsExactProducer(t *testing.T) {
	graph := ordinaryMaterialGraph()
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	base, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := ordinaryMaterialGraph()
	changed.Nodes[0].Materials[0].Binding = "renamed-runtime"
	digest, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest == base {
		t.Fatal("material binding did not change ordinary build identity")
	}
}

func TestOCIHermeticMaterialGraphRejectsEdgeSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Graph)
		code   e.Code
	}{
		{name: "missing edge", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Needs = []string{"fetch-kubernetes-abc"}
		}},
		{name: "undeclared edge", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Materials = nil
		}},
		{name: "wrong component", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Materials[0].Component = "other"
		}},
		{name: "BuildKit consumer", code: e.CodeEngineCapability, mutate: func(graph *Graph) {
			graph.Nodes[0].BuilderType = BuilderBuildKit
			graph.Nodes[0].Network = NetworkDefault
			graph.Nodes[0].OutputType = "oci-image"
		}},
		{name: "Nix consumer", code: e.CodeEngineCapability, mutate: func(graph *Graph) {
			graph.Nodes[0].BuilderType = BuilderNix
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := ordinaryMaterialGraph()
			test.mutate(&graph)
			assertIRCode(t, graph.Validate(), test.code)
		})
	}
}
