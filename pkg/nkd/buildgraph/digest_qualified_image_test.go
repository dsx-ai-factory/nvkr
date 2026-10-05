package buildgraph

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestCompileDigestQualifiedImageUsesNativeSubject(t *testing.T) {
	for _, test := range []struct{ name, ref, want string }{
		{"digest", "registry.example/tool@" + digAmd, "registry.example/tool"},
		{"tag-and-digest", "registry.example/tool:latest@" + digAmd, "registry.example/tool"},
		{"explicit-scheme", "oci://registry.example/tool@" + digAmd, "registry.example/tool"},
		{"tag", "registry.example/tool:1.2.3", "registry.example/tool:1.2.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat := &catalog.Catalog{Components: map[string]catalog.Component{"tool": {
				Platforms: []string{"linux/amd64"}, OutputType: "oci-image", Tag: "selection",
				Prebuilt: &catalog.Prebuilt{Image: test.ref, Digests: map[string]string{"amd64": digArm}, Evidence: &catalog.ExternalEvidence{None: true}},
			}}}
			resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
			if err != nil {
				t.Fatal(err)
			}
			node := mustCompile(t, resolved).Nodes[0]
			if node.Kind != ir.KindPull || node.Artifact != test.want || node.Digest != digArm {
				t.Fatalf("wrong native subject or transport: %+v", node)
			}
		})
	}
}
