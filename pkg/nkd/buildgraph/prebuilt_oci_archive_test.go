package buildgraph

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestCompilePrebuiltOCIArchivePreservesBlobPin(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			ref := "registry.example/charts/postgresql@" + digAmd
			cat := &catalog.Catalog{Components: map[string]catalog.Component{
				"postgresql": {Platforms: []string{"linux/amd64", "linux/arm64"}, OutputType: "raw", Tag: "16.2.0", Prebuilt: &catalog.Prebuilt{
					OCIArchive: ref, Digests: map[string]string{"amd64": digAmd, "arm64": digAmd}, Evidence: &catalog.ExternalEvidence{None: true},
				}},
			}}
			selection, err := Resolve(cat, Selection{Components: []string{"postgresql"}, Platform: platform})
			if err != nil {
				t.Fatal(err)
			}
			graph := mustCompile(t, selection)
			if len(graph.Nodes) != 2 {
				t.Fatalf("expected one pull and terminal verify, got %v", graph.Nodes)
			}
			node := graph.Nodes[0]
			if node.Kind != ir.KindPull || node.Artifact != ir.OCIBlobScheme+ref || node.Digest != digAmd || node.OutputType != "raw" || node.ArtifactPath != "postgresql" || len(node.Env) != 0 {
				t.Fatalf("chart archive lowered as wrong artifact kind or bytes: %+v", node)
			}
		})
	}
}
