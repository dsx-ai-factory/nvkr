package buildgraph

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestImageParentBindingIsContentNotRepositoryIdentity(t *testing.T) {
	compile := func(repository, parent string) *ir.Graph {
		t.Helper()
		cat := &catalog.Catalog{Components: map[string]catalog.Component{"tool": {
			Platforms: []string{"linux/amd64"}, OutputType: "oci-image", Tag: "v1.0.0",
			Prebuilt: &catalog.Prebuilt{Image: repository + "@" + parent, ImageParentDigest: parent, Digests: map[string]string{"amd64": digAmd}, Evidence: &catalog.ExternalEvidence{None: true}},
		}}}
		resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
		if err != nil {
			t.Fatal(err)
		}
		graph, err := Compile(resolved)
		if err != nil {
			t.Fatal(err)
		}
		return graph
	}
	first := compile("registry.example/tool", digArm)
	mirror := compile("mirror.example/tool", digArm)
	other := compile("registry.example/tool", "sha256:"+strings.Repeat("c", 64))
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	mirrorDigest, err := mirror.Digest()
	if err != nil {
		t.Fatal(err)
	}
	otherDigest, err := other.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != mirrorDigest {
		t.Fatal("repository transport entered logical identity")
	}
	if firstDigest == otherDigest {
		t.Fatal("authenticated content-selection constraint omitted from logical identity")
	}
	firstTransport, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	mirrorTransport, err := mirror.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstTransport, mirrorTransport) {
		t.Fatal("full transport lost exact repository")
	}
}
