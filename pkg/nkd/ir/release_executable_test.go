package ir

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func releaseExecutableGraph() Graph {
	g := pullGraph()
	g.Nodes[0].OutputType = "host-package"
	g.Nodes[0].Env = []EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}}
	g.Nodes[0].ExecutableArchive = &ExecutableArchive{Digest: "sha256:" + strings.Repeat("b", 64), Member: "bin/tool"}
	return g
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
