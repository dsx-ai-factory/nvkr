// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func runtimeConsumerFixture() (*Resolved, map[string]execution.HostPlatform, map[string]ir.RuntimeSelection) {
	component := testVMImageComponent([]execution.HostPlatform{execution.HostPlatformLinuxAMD64}, execution.CapabilityRequired)
	component.Platforms, component.Tag = []string{"linux/amd64"}, "v1.0.0"
	component.ToolchainImages = map[string]string{"amd64": testVMToolchain}
	component.BuildCommand = nil
	component.BuildNetwork = ir.NetworkDefault
	component.Packer = &catalog.PackerConfig{ProjectPath: ".", PackerVersion: "1.16.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"}
	return &Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{{Name: "image", Component: component}}},
		map[string]execution.HostPlatform{"image": execution.HostPlatformLinuxAMD64},
		map[string]ir.RuntimeSelection{"image": {Tool: "runtime", Component: "vm-runtime", Platform: "linux/amd64",
			BuildDigest: "sha256:" + strings.Repeat("a", 64), Invocation: ir.RuntimeInvocation}}
}

func TestProducedRuntimeConsumerHasNoImageAcquisitionDependency(t *testing.T) {
	resolved, hosts, runtimes := runtimeConsumerFixture()
	graph, err := CompileForRuntimes(resolved, hosts, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	before, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Toolchain != "" || node.VendorImage != "" {
			t.Fatal("runtime consumer retained old image dependency")
		}
		if node.Kind == ir.KindBuild && (node.Runtime == nil || *node.Runtime != runtimes["image"]) {
			t.Fatal("consumer lost selected runtime")
		}
	}
	resolved.Components[0].ToolchainImages["amd64"] = "example.test/unused@sha256:" + strings.Repeat("f", 64)
	afterGraph, err := CompileForRuntimes(resolved, hosts, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	after, err := afterGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unused retired image changed consumer identity")
	}
	selection := runtimes["image"]
	selection.BuildDigest = "sha256:" + strings.Repeat("b", 64)
	runtimes["image"] = selection
	changed, err := CompileForRuntimes(resolved, hosts, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest == before {
		t.Fatal("runtime producer repin did not change consumer identity")
	}
}

func TestProducedRuntimeCompilerRejectsIncompleteConsumerSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Resolved, map[string]ir.RuntimeSelection)
	}{
		{"unknown consumer", func(_ *Resolved, s map[string]ir.RuntimeSelection) { s["other"] = s["image"] }},
		{"wrong host", func(_ *Resolved, s map[string]ir.RuntimeSelection) {
			v := s["image"]
			v.Platform = "linux/arm64"
			s["image"] = v
		}},
		{"container vendoring", func(r *Resolved, _ map[string]ir.RuntimeSelection) { r.Components[0].VendorCommand = []string{"true"} }},
		{"command consumer", func(r *Resolved, _ map[string]ir.RuntimeSelection) {
			r.Components[0].Packer = nil
			r.Components[0].BuildCommand = []string{"true"}
			r.Components[0].BuildNetwork = ir.NetworkNone
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, hosts, runtimes := runtimeConsumerFixture()
			tc.change(resolved, runtimes)
			graph, err := CompileForRuntimes(resolved, hosts, runtimes)
			if code(err) != e.CodeEngineCapability || graph != nil {
				t.Fatalf("compiler = %v, %v", graph, err)
			}
		})
	}
}
