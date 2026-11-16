// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func buildToolGraph() Graph {
	g := sample()
	g.Nodes[0].BuildTool = &BuildToolBinding{Name: "nfpm-amd64", Platform: "linux/amd64", Format: "tar-gzip", Member: "nfpm",
		ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}
	g.Nodes[0].BuildToolURL = "https://example.test/nfpm.tar.gz"
	g.Nodes[0].Command = []string{"make", "NFPM_BIN=" + BuildToolPath}
	return g
}

func TestBuildToolIdentity(t *testing.T) {
	base, err := buildToolGraph().Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Node)
		moves  bool
	}{
		{"transport", func(n *Node) { n.BuildToolURL = "https://mirror.test/tool" }, false},
		{"archive", func(n *Node) { n.BuildTool.ArchiveDigest = "sha256:" + strings.Repeat("c", 64) }, true},
		{"executable", func(n *Node) { n.BuildTool.ExecutableDigest = "sha256:" + strings.Repeat("c", 64) }, true},
		{"member", func(n *Node) { n.BuildTool.Member = "bin/nfpm" }, true},
		{"name", func(n *Node) { n.BuildTool.Name = "other" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := buildToolGraph()
			test.change(&g.Nodes[0])
			got, err := g.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if (got != base) != test.moves {
				t.Fatal("unexpected Build identity change")
			}
		})
	}
}

func TestBuildToolAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Node)
		code   e.Code
	}{
		{"native", func(*Node) {}, ""},
		{"wrong architecture", func(n *Node) { n.BuildTool.Platform = "linux/arm64" }, e.CodeEngineCapability},
		{"network", func(n *Node) { n.Network = NetworkDefault }, e.CodeEngineCapability},
		{"writable source", func(n *Node) { n.SourceReadOnly = false }, e.CodeEngineCapability},
		{"missing binding", func(n *Node) { n.BuildTool = nil }, e.CodeUnpinnedSource},
		{"missing executable pin", func(n *Node) { n.BuildTool.ExecutableDigest = "" }, e.CodeUnpinnedSource},
		{"mutable transport", func(n *Node) { n.BuildToolURL = "/tmp/nfpm" }, e.CodeUnpinnedSource},
		{"path lookup", func(n *Node) { n.Command = []string{"make", "NFPM_BIN=nfpm"} }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := buildToolGraph()
			test.change(&g.Nodes[0])
			if err := g.Validate(); pinnedToolCode(err) != test.code {
				t.Fatalf("got %v; want %s", err, test.code)
			}
		})
	}
}
