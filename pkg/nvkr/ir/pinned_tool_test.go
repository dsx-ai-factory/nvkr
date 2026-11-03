// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func pinnedToolGraph() Graph {
	recipe := Node{ID: "build-recipe", Kind: KindBuild, Component: "recipe", BuilderType: BuilderPinnedTool, Tool: "aicr",
		Artifact: "https://example.test/aicr.tar.gz", Digest: "sha256:" + strings.Repeat("b", 64),
		ExecutableArchive: &ExecutableArchive{Format: "tar-gzip", Digest: "sha256:" + strings.Repeat("a", 64), Member: "aicr"},
		Toolchain:         "example.test/runner@sha256:" + strings.Repeat("c", 64), Platform: "linux/arm64", Network: NetworkNone,
		Env:     []EnvVar{{Name: "AICR_LOG_FORMAT", Value: "text"}},
		Command: []string{"recipe", "--output", PinnedToolOutputDir + "/recipe.yaml"}, Workdir: PinnedToolWorkdir,
		OutputType: OutputFile, ArtifactPath: "recipe.yaml", Version: "1.2.0"}
	bundle := recipe
	bundle.ID, bundle.Component, bundle.Needs = "build-bundle", "bundle", []string{"build-recipe"}
	bundle.Materials = []MaterialBinding{{Binding: "recipe", Component: "recipe", Producer: "build-recipe"}}
	bundle.Command = []string{"bundle", "--recipe", PinnedToolInputsRoot + "/materials/recipe", "--output", "oci-layout:" + PinnedToolOutputDir}
	bundle.OutputType, bundle.ArtifactPath = OutputOCILayout, ""
	return Graph{SchemaVersion: SchemaVersion, Platform: "linux/arm64", Nodes: []Node{
		bundle, recipe, {ID: "verify", Kind: KindVerify, Needs: []string{"build-bundle", "build-recipe"}},
	}}
}

func TestPinnedToolNodeContract(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Node, *Node)
		code   e.Code
	}{
		{name: "source-less producer and material chain"},
		{name: "source fetch field", change: func(_, r *Node) { r.SourceDest = "source-x" }, code: e.CodeInvalidCatalog},
		{name: "network", change: func(_, r *Node) { r.Network = NetworkDefault }, code: e.CodeInvalidCatalog},
		{name: "unrendered token", change: func(_, r *Node) { r.Command[2] = "{{.Output}}" }, code: e.CodeInvalidCatalog},
		{name: "executor-owned environment", change: func(_, r *Node) { r.Env = []EnvVar{{Name: "HOME", Value: "/root"}} }, code: e.CodeInvalidCatalog},
		{name: "fixed workdir", change: func(_, r *Node) { r.Workdir = "/src" }, code: e.CodeInvalidCatalog},
		{name: "missing tool name", change: func(_, r *Node) { r.Tool = "" }, code: e.CodeUnpinnedSource},
		{name: "missing archive pin", change: func(_, r *Node) { r.ExecutableArchive = nil }, code: e.CodeUnpinnedSource},
		{name: "raw archive digest differs", change: func(_, r *Node) { r.ExecutableArchive.Format = "raw" }, code: e.CodeUnpinnedSource},
		{name: "assignment in member", change: func(_, r *Node) { r.ExecutableArchive.Member = "bin/aicr=1" }, code: e.CodeUnpinnedSource},
		{name: "tagged toolchain", change: func(_, r *Node) { r.Toolchain = "example.test/runner:1" }, code: e.CodeTagToolchain},
		{name: "directory output form", change: func(_, r *Node) { r.OutputType = "raw" }, code: e.CodeInvalidCatalog},
		{name: "nested file", change: func(_, r *Node) { r.ArtifactPath = "a/recipe.yaml" }, code: e.CodeInvalidCatalog},
		{name: "undeclared dependency", change: func(b, _ *Node) { b.Materials = nil }, code: e.CodeInvalidCatalog},
		{name: "layout material", change: func(b, r *Node) {
			r.OutputType, r.ArtifactPath = OutputOCILayout, ""
		}, code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := pinnedToolGraph()
			graph.Nodes[1].Command = append([]string(nil), graph.Nodes[1].Command...)
			archive := *graph.Nodes[1].ExecutableArchive
			graph.Nodes[1].ExecutableArchive = &archive
			if test.change != nil {
				test.change(&graph.Nodes[0], &graph.Nodes[1])
			}
			err := graph.Validate()
			if got := pinnedToolCode(err); got != test.code {
				t.Fatalf("Validate() error %v; want %s", err, test.code)
			}
		})
	}
}

func TestPinnedToolTransportStaysOutOfBuildIdentity(t *testing.T) {
	graph := pinnedToolGraph()
	base, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	graph.Nodes[1].Artifact = "https://mirror.example/aicr.tar.gz"
	moved, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	graph.Nodes[1].Tool = "other"
	renamed, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if moved != base || renamed == base {
		t.Fatalf("transport changed identity (%t) or tool name did not (%t)", moved != base, renamed == base)
	}
}

func pinnedToolCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	if err != nil {
		return "uncoded"
	}
	return ""
}
