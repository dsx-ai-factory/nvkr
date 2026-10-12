// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const (
	testSourceCommit = "70d3cc986aa8221cd1dfb1121852688902d3bf53"
	testSourceTree   = "8f82f106c65896250424161432791051e9f2cd99"
)

func testBuildEnv(arch string) []ir.EnvVar {
	env := []ir.EnvVar{
		{Name: "CGO_ENABLED", Value: "0"},
		{Name: "GOARCH", Value: arch},
		{Name: "GOFLAGS", Value: "-trimpath -buildvcs=false"},
		{Name: "GOOS", Value: "linux"},
		{Name: "GOTOOLCHAIN", Value: "local"},
		{Name: "LC_ALL", Value: "C"},
		{Name: "SOURCE_DATE_EPOCH", Value: "1"},
		{Name: "TZ", Value: "UTC"},
	}
	if arch == "arm64" {
		env = append(env, ir.EnvVar{Name: "GOARM64", Value: "v8.0"})
	} else {
		env = append(env, ir.EnvVar{Name: "GOAMD64", Value: "v1"})
	}
	// Node validation intentionally requires the canonical lexical order.
	for i := 1; i < len(env); i++ {
		for j := i; j > 0 && env[j].Name < env[j-1].Name; j-- {
			env[j], env[j-1] = env[j-1], env[j]
		}
	}
	return env
}

func testFetchNode(id, repo, commit, tree, dest, toolchain, _ string) ir.Node {
	vendorSpec, err := ir.VendorSpecIdentity(nil, toolchain)
	if err != nil {
		panic(err)
	}
	return ir.Node{
		ID: id, Kind: ir.KindFetch, Repo: repo, Commit: commit, TreeHash: tree,
		Dest: dest, VendorImage: toolchain, VendorSpecDigest: vendorSpec,
		Network: ir.NetworkDefault,
	}
}

func completeTestBuild(node ir.Node, fetch ir.Node) ir.Node {
	if node.BuilderType == "" {
		node.BuilderType = ir.BuilderOCIHermetic
	}
	if node.Toolchain == "" {
		node.Toolchain = "registry.example/toolchain@sha256:" + strings.Repeat("c", 64)
	}
	if len(node.Command) == 0 {
		node.Command = []string{"must-not-run"}
	}
	if node.WriteDir == "" {
		node.WriteDir = "out"
	}
	if node.ArtifactPath == "" {
		node.ArtifactPath = node.WriteDir + "/" + node.Component
	}
	node.Needs = []string{fetch.ID}
	node.Platform = "linux/amd64"
	node.Network = ir.NetworkNone
	node.Env = testBuildEnv("amd64")
	node.Workdir = "/src"
	node.SourceDest = fetch.Dest
	node.SourceCommit = fetch.Commit
	node.SourceTreeHash = fetch.TreeHash
	node.VendorSpecDigest = fetch.VendorSpecDigest
	node.SourceReadOnly = true
	if node.OutputType == "" {
		node.OutputType = "raw"
	}
	if node.Version == "" {
		node.Version = "1.0.0"
	}
	return node
}

func testSourceGraph(fetch ir.Node, builds ...ir.Node) *ir.Graph {
	graph := &ir.Graph{
		SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64",
	}
	verify := ir.Node{ID: "verify", Kind: ir.KindVerify}
	for _, build := range builds {
		graph.Nodes = append(graph.Nodes, build)
		verify.Needs = append(verify.Needs, build.ID)
	}
	graph.Nodes = append(graph.Nodes, fetch, verify)
	graph.Normalize()
	return graph
}

func mustGraphDigest(t *testing.T, graph *ir.Graph) string {
	t.Helper()
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func sampleGraph() *ir.Graph {
	commit := strings.Repeat("c", 40)
	tree := strings.Repeat("e", 40)
	toolchain := "golang@sha256:" + strings.Repeat("d", 64)
	vendorSpec, err := ir.VendorSpecIdentity(nil, toolchain)
	if err != nil {
		panic(err)
	}
	return &ir.Graph{
		SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64",
		Nodes: []ir.Node{
			{ID: "build-kubectl", Kind: ir.KindBuild, Component: "kubectl", Needs: []string{"fetch-k"},
				BuilderType: ir.BuilderOCIHermetic, Toolchain: toolchain,
				Platform: "linux/amd64", Network: ir.NetworkNone, Workdir: "/src",
				Env: []ir.EnvVar{
					{Name: "CGO_ENABLED", Value: "0"}, {Name: "GOAMD64", Value: "v1"},
					{Name: "GOARCH", Value: "amd64"}, {Name: "GOFLAGS", Value: "-trimpath -buildvcs=false"},
					{Name: "GOOS", Value: "linux"}, {Name: "GOTOOLCHAIN", Value: "local"},
					{Name: "LC_ALL", Value: "C"}, {Name: "SOURCE_DATE_EPOCH", Value: "1"}, {Name: "TZ", Value: "UTC"},
				},
				Command: []string{"make", "kubectl"}, SourceDest: "k-src", SourceCommit: commit,
				SourceTreeHash: tree, VendorSpecDigest: vendorSpec, SourceReadOnly: true,
				WriteDir: "out", ArtifactPath: "out/kubectl", OutputType: "host-package", Version: "1.32.0"},
			{ID: "fetch-k", Kind: ir.KindFetch, Repo: "https://x/k", Commit: commit, TreeHash: tree,
				Dest: "k-src", VendorImage: toolchain, VendorSpecDigest: vendorSpec, Network: ir.NetworkDefault},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"build-kubectl"}},
		},
	}
}
