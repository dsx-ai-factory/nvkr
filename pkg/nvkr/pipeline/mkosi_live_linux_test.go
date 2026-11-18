// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

// This opt-in test runs the real acquisition and native assembly boundary. It
// does not claim a completed Run, SBOM publication, consumer use or disk output.
func TestNativeMkosiAssembly(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_MKOSI") != "1" {
		t.Skip("requires an explicitly prepared native VM assembly fixture")
	}
	if runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		t.Fatal("native proof requires Linux amd64 and privileged launcher setup")
	}
	scratch, catalogPath := os.Getenv("NVKR_TEST_MKOSI_SCRATCH"), os.Getenv("NVKR_TEST_MKOSI_CATALOG")
	if !filepath.IsAbs(scratch) || !filepath.IsAbs(catalogPath) {
		t.Fatal("proof requires explicit absolute scratch and catalog paths")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	cat, err := catalog.LoadContext(ctx, catalog.Dir(catalogPath))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Platform: "linux/amd64", OutputFormat: "oci",
		Components: []string{"packer-amd64", "packer-plugin-qemu-amd64", "vm-runtime-amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(scratch, "acquisition")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(state)
	workspace, err := HoldSourceWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspace.Close(); err != nil {
			t.Error(err)
		}
	}()
	opts := RunOptions{Ctx: ctx, SourceRoot: "src", Launch: true, Stdout: os.Stdout, Stderr: os.Stderr}
	results := make(map[string]*ArtifactResult)
	var producer ir.Node
	for _, node := range graph.Nodes {
		switch node.Kind {
		case ir.KindFetch:
			fetchOptions := opts
			fetchOptions.SourceWorkspace = workspace
			if _, err := (Local{}).ExecNode(node, fetchOptions); err != nil {
				t.Fatal(err)
			}
		case ir.KindPull:
			result, err := (Local{}).ExecNode(node, opts)
			if err != nil {
				t.Fatal(err)
			}
			results[node.Component] = result
		case ir.KindBuild:
			if node.BuilderType != ir.BuilderMkosi || producer.ID != "" {
				t.Fatal("proof catalog has an unexpected producer")
			}
			producer = node
		}
	}
	if producer.ID == "" {
		t.Fatal("proof catalog has no mkosi producer")
	}
	var observations []material.Observation
	var inputs []BuildMaterialInput
	for _, binding := range producer.Materials {
		result := results[binding.Component]
		if result == nil {
			t.Fatal("material producer did not complete")
		}
		content, err := heldfile.OpenContext(ctx, result.AbsPath, heldfile.Options{Label: "native-mkosi-material",
			MaxBytes: MaxBuildMaterialBytes, ExpectedDigest: result.SHA256})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = content.Close() })
		path, err := material.StagingPath(binding.Binding)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, BuildMaterialInput{Path: path, SHA256: result.SHA256, Content: content})
		observations = append(observations, material.Observation{Binding: binding.Binding, Component: binding.Component,
			Kind: result.OutputType, Architecture: "amd64", Digest: result.SHA256})
	}
	manifest, err := material.NewManifest(observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	held, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "native-mkosi-manifest", MaxBytes: maxBuildMaterialManifestBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	opts.BuildMaterials, err = NewBuildMaterials(held, inputs)
	if err != nil {
		t.Fatal(err)
	}
	opts.MkosiScratch = scratch
	result, produced, err := (Local{}).ExecuteProducedRuntime(graph, digest, producer.Component, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := produced.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := produced.AuthenticateResultContext(ctx, *result); err != nil {
		t.Fatal(err)
	}
	binding, err := produced.BindingContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("concrete runtime production: %s", raw)
}
