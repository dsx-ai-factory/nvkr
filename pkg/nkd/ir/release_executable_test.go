package ir

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func releaseExecutableGraph() Graph {
	g := pullGraph()
	g.Nodes[0].OutputType = "host-package"
	g.Nodes[0].Env = []EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}}
	g.Nodes[0].ExecutableArchive = &ExecutableArchive{Digest: "sha256:" + strings.Repeat("b", 64), Member: "bin/tool"}
	return g
}

func TestReleaseExecutableCanonicalVector(t *testing.T) {
	const logical = `{"nodes":[{"artifactPath":"tool","component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":[{"name":"SOURCE_DATE_EPOCH","value":"1"}],"executableArchive":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","member":"bin/tool"},"id":"pull-tool","kind":"pull","network":"default","outputType":"host-package","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v4"}`
	// Independently measured SHA-256 of UTF8("nkd.build") + NUL + logical.
	const digest = "sha256:0576d2722404b28f3d0e61251a523359b900679669fd0afa3f299101fc1ab32c"
	graph := releaseExecutableGraph()
	view, err := graph.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(view)
	if err != nil || string(raw) != logical {
		t.Fatalf("archive logical vector: %s, %v", raw, err)
	}
	got, err := graph.Digest()
	if err != nil || got != digest {
		t.Fatalf("archive identity vector: %s, %v", got, err)
	}
}

func TestReleaseExecutableRejectsMalformedPullClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Node)
		code   e.Code
	}{
		{name: "unpinned archive", change: func(n *Node) { n.ExecutableArchive.Digest = "" }},
		{name: "malformed archive digest", change: func(n *Node) { n.ExecutableArchive.Digest = "sha256:abc" }},
		{name: "absolute member", change: func(n *Node) { n.ExecutableArchive.Member = "/bin/tool" }},
		{name: "traversal member", change: func(n *Node) { n.ExecutableArchive.Member = "../tool" }},
		{name: "empty member", change: func(n *Node) { n.ExecutableArchive.Member = "" }},
		{name: "OCI transport", change: func(n *Node) { n.Artifact = "example.invalid/image:v1" }},
		{name: "HTTP transport", change: func(n *Node) { n.Artifact = "http://example.invalid/tool.tar.gz" }},
		{name: "unpinned executable", change: func(n *Node) { n.Digest = "" }, code: e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := releaseExecutableGraph().Nodes[0]
			tc.change(&node)
			code := tc.code
			if code == "" {
				code = e.CodeInvalidCatalog
			}
			assertIRCode(t, node.Validate(), code)
		})
	}
}

func TestReleaseExecutableIdentityAndDescriptionIsolation(t *testing.T) {
	base := releaseExecutableGraph()
	want, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Node)
		moves  bool
	}{
		{name: "archive pin", change: func(n *Node) { n.ExecutableArchive.Digest = "sha256:" + strings.Repeat("c", 64) }, moves: true},
		{name: "member", change: func(n *Node) { n.ExecutableArchive.Member = "other/tool" }, moves: true},
		{name: "executable pin", change: func(n *Node) { n.Digest = "sha256:" + strings.Repeat("c", 64) }, moves: true},
		{name: "mirror", change: func(n *Node) { n.Artifact = "https://mirror.example/tool.tar.gz" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := releaseExecutableGraph()
			test.change(&g.Nodes[0])
			got, err := g.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if (got != want) != test.moves {
				t.Fatalf("identity movement %t; want %t", got != want, test.moves)
			}
		})
	}
	description, err := base.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	description.Nodes[0].ExecutableArchive.Member = "changed"
	if base.Nodes[0].ExecutableArchive.Member != "bin/tool" {
		t.Fatal("logical description aliases source graph")
	}
}

func TestReleaseExecutableRejectsWrongNodeKindAndArchive(t *testing.T) {
	for _, test := range []struct {
		name string
		node Node
	}{
		{name: "fetch", node: sample().Nodes[1]},
		{name: "build", node: sample().Nodes[0]},
		{name: "verify", node: sample().Nodes[2]},
		{name: "raw output", node: validRawPullNode()},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.node.ExecutableArchive = &ExecutableArchive{Digest: "sha256:" + strings.Repeat("b", 64), Member: "bin/tool"}
			assertIRCode(t, test.node.Validate(), e.CodeInvalidCatalog)
		})
	}
}
