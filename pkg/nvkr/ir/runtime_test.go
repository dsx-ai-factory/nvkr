// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func runtimeGraph(t *testing.T) Graph {
	t.Helper()
	graph := vmGraph(t)
	graph.Nodes[0].Toolchain = ""
	graph.Nodes[0].Runtime = &RuntimeSelection{Tool: "runtime", Component: "vm-runtime",
		BuildDigest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Invocation: RuntimeInvocation}
	return graph
}

func TestRuntimeSelectionBindsNamedProducerIntoBuild(t *testing.T) {
	base := runtimeGraph(t)
	want, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*RuntimeSelection)
	}{
		{"requirement", func(s *RuntimeSelection) { s.Tool = "sibling" }},
		{"producer", func(s *RuntimeSelection) { s.BuildDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"component", func(s *RuntimeSelection) { s.Component = "other-runtime" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := runtimeGraph(t)
			tc.change(graph.Nodes[0].Runtime)
			got, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("changed runtime retained consumer Build identity")
			}
		})
	}
	copy := base.normalizedCopy()
	copy.Nodes[0].Runtime.Tool = "other"
	logical := base.logicalGraph()
	logical.Nodes[0].Runtime.Tool = "another"
	if base.Nodes[0].Runtime.Tool != "runtime" {
		t.Fatal("runtime selection aliases a graph projection")
	}
}

func TestRuntimeSelectionRejectsAmbiguousOrUnsupportedConsumers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Graph)
		code   e.Code
	}{
		{"image and runtime", func(g *Graph) { g.Nodes[0].Toolchain = g.Nodes[1].VendorImage }, e.CodeEngineCapability},
		{"foreign host", func(g *Graph) { g.Nodes[0].ToolchainPlatform = "linux/arm64" }, e.CodeEngineCapability},
		{"unknown invocation", func(g *Graph) { g.Nodes[0].Runtime.Invocation = "other/v1" }, e.CodeEngineCapability},
		{"arm64 runtime deferred", func(g *Graph) { g.Nodes[0].Runtime.Platform = "linux/arm64" }, e.CodeEngineCapability},
		{"missing producer pin", func(g *Graph) { g.Nodes[0].Runtime.BuildDigest = "" }, e.CodeUnpinnedSource},
		{"unknown requirement", func(g *Graph) { g.Nodes[0].Runtime.Tool = "../runtime" }, e.CodeUnpinnedSource},
		{"fetch runtime", func(g *Graph) { g.Nodes[1].Runtime = g.Nodes[0].Runtime }, e.CodeInvalidCatalog},
		{"verify runtime", func(g *Graph) { g.Nodes[3].Runtime = g.Nodes[0].Runtime }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := runtimeGraph(t)
			tc.change(&graph)
			assertIRCode(t, graph.Validate(), tc.code)
		})
	}
}
