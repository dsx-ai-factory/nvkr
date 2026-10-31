// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func releaseExecutableGraph() Graph {
	g := pullGraph()
	g.Nodes[0].OutputType = "host-package"
	g.Nodes[0].Env = []EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}}
	g.Nodes[0].ExecutableArchive = &ExecutableArchive{Digest: "sha256:" + strings.Repeat("b", 64), Member: "bin/tool"}
	return g
}

func TestReleaseExecutableCanonicalVector(t *testing.T) {
	const logical = `{"nodes":[{"artifactPath":"tool","component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":[{"name":"SOURCE_DATE_EPOCH","value":"1"}],"executableArchive":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","member":"bin/tool"},"id":"pull-tool","kind":"pull","network":"default","outputType":"host-package","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nvkr.dev/ir/v4"}`
	// Independently measured SHA-256 of UTF8("nvkr.build") + NUL + logical.
	const digest = "sha256:4a8e7fabf683d7ae5c94cc3a9d554e5a153a0d62d02416edb160d959ca214ce9"
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
		{name: "unknown format", change: func(n *Node) { n.ExecutableArchive.Format = "7z" }},
		{name: "unpinned archive", change: func(n *Node) { n.ExecutableArchive.Digest = "" }},
		{name: "malformed archive digest", change: func(n *Node) { n.ExecutableArchive.Digest = "sha256:abc" }},
		{name: "absolute member", change: func(n *Node) { n.ExecutableArchive.Member = "/bin/tool" }},
		{name: "traversal member", change: func(n *Node) { n.ExecutableArchive.Member = "../tool" }},
		{name: "empty member", change: func(n *Node) { n.ExecutableArchive.Member = "" }},
		{name: "OCI transport", change: func(n *Node) { n.Artifact = "example.invalid/image:v1" }},
		{name: "HTTP transport", change: func(n *Node) { n.Artifact = "http://example.invalid/tool.tar.gz" }},
		{name: "unpinned executable", change: func(n *Node) { n.Digest = "" }, code: e.CodeUnpinnedSource},
		{name: "unpinned companion", change: func(n *Node) { n.ExecutableArchive.Companions[0].Digest = "" }},
		{name: "traversal companion", change: func(n *Node) { n.ExecutableArchive.Companions[0].Member = "../tool.sig" }},
		{name: "companion shadows component", change: func(n *Node) { n.ExecutableArchive.Companions[0].Member = "dist/tool" }},
		{name: "companion shadows member", change: func(n *Node) {
			n.Component, n.ID = "renamed", "pull-renamed"
			n.ExecutableArchive.Companions[0].Member = "dist/tool"
		}},
		{name: "unsorted companions", change: func(n *Node) {
			n.ExecutableArchive.Companions = append(n.ExecutableArchive.Companions, ArchiveMember{Member: "a.sig", Digest: n.Digest})
		}},
		{name: "colliding companions", change: func(n *Node) {
			n.ExecutableArchive.Companions = append(n.ExecutableArchive.Companions, ArchiveMember{Member: "x/tool.sig", Digest: n.Digest})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := releaseExecutableGraph().Nodes[0]
			node.ExecutableArchive.Companions = []ArchiveMember{{Member: "tool.sig", Digest: "sha256:" + strings.Repeat("d", 64)}}
			if err := node.Validate(); err != nil {
				t.Fatalf("baseline companion rejected: %v", err)
			}
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
		{name: "archive format", change: func(n *Node) { n.ExecutableArchive.Format = "zip" }, moves: true},
		{name: "archive pin", change: func(n *Node) { n.ExecutableArchive.Digest = "sha256:" + strings.Repeat("c", 64) }, moves: true},
		{name: "member", change: func(n *Node) { n.ExecutableArchive.Member = "other/tool" }, moves: true},
		{name: "executable pin", change: func(n *Node) { n.Digest = "sha256:" + strings.Repeat("c", 64) }, moves: true},
		{name: "companion", change: func(n *Node) {
			n.ExecutableArchive.Companions = []ArchiveMember{{Member: "tool.sig", Digest: "sha256:" + strings.Repeat("d", 64)}}
		}, moves: true},
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
	base.Nodes[0].ExecutableArchive.Companions = []ArchiveMember{{Member: "tool.sig", Digest: "sha256:" + strings.Repeat("d", 64)}}
	description, err = base.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	description.Nodes[0].ExecutableArchive.Companions[0].Member = "changed"
	if base.Nodes[0].ExecutableArchive.Companions[0].Member != "tool.sig" {
		t.Fatal("logical description aliases source companions")
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
