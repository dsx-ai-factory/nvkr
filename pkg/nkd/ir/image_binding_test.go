package ir

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestImageParentExecutableAdmission(t *testing.T) {
	parent := "sha256:" + strings.Repeat("b", 64)
	child := "sha256:" + strings.Repeat("a", 64)
	base := Node{ID: "pull-tool", Kind: KindPull, Component: "tool", Artifact: "registry.example/tool@" + parent,
		ImageParentDigest: parent, Digest: child, Platform: "linux/amd64", Network: NetworkDefault, OutputType: "oci-image", Version: "1.0.0"}
	for _, tc := range []struct {
		name   string
		change func(*Node)
		code   e.Code
	}{
		{"exact", func(*Node) {}, ""},
		{"malformed parent", func(n *Node) { n.ImageParentDigest = "invalid" }, e.CodeUnverifiedRef},
		{"mismatched parent", func(n *Node) { n.ImageParentDigest = child }, e.CodeUnverifiedRef},
		{"missing index binding", func(n *Node) { n.ImageParentDigest = "" }, e.CodeUnverifiedRef},
		{"parent on raw", func(n *Node) {
			n.OutputType = "raw"
			n.Artifact = "https://example.invalid/tool"
			n.ArtifactPath = "tool"
		}, e.CodeInvalidCatalog},
		{"parent on verify", func(n *Node) { *n = Node{ID: "verify", Kind: KindVerify, ImageParentDigest: parent} }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := base
			tc.change(&node)
			err := node.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertIRCode(t, err, tc.code)
			}
		})
	}
	source := sample()
	source.Nodes[0].ImageParentDigest = parent
	assertIRCode(t, source.Validate(), e.CodeInvalidCatalog)
	source = sample()
	source.Nodes[1].ImageParentDigest = parent
	assertIRCode(t, source.Validate(), e.CodeInvalidCatalog)
}

func TestImageParentOptionalFieldPreservesLegacyLogicalBytes(t *testing.T) {
	node := Node{ID: "pull-tool", Kind: KindPull, Component: "tool", Artifact: "registry.example/tool:v1",
		Digest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: NetworkDefault, OutputType: "oci-image", Version: "1.0.0"}
	graph := Graph{SchemaVersion: SchemaVersion, Platform: node.Platform, Nodes: []Node{node, {ID: "verify", Kind: KindVerify, Needs: []string{node.ID}}}}
	logical, err := graph.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := canonical.Marshal(logical)
	if err != nil {
		t.Fatal(err)
	}
	// Frozen pre-extension v4 logical bytes, not a new-field DTO derived oracle.
	want := []byte(`{"nodes":[{"component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id":"pull-tool","kind":"pull","network":"default","outputType":"oci-image","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v4"}`)
	if !bytes.Equal(actual, want) {
		t.Fatalf("legacy v4 logical bytes changed: %s", actual)
	}
}
