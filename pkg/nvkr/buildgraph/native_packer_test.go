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

func TestNativePackerSeparatesContainerVendoringAndHostExecution(t *testing.T) {
	native := "sha256:" + strings.Repeat("c", 64)
	for _, row := range []struct {
		name      string
		host      execution.HostPlatform
		toolchain string
		want      string
		code      e.Code
	}{
		{"Mac native", execution.HostPlatformDarwinARM64, native, native, ""},
		{"Mac missing native bundle", execution.HostPlatformDarwinARM64, testVMToolchain, "", e.CodeUnpinnedSource},
		{"Linux container unchanged", execution.HostPlatformLinuxARM64, native, testVMToolchain, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			component := testVMImageComponent([]execution.HostPlatform{row.host}, execution.CapabilityPreferred)
			component.Platforms, component.Tag = []string{"linux/arm64"}, "v1.0.0"
			component.ToolchainImage = row.toolchain
			component.ToolchainImages = map[string]string{"arm64": testVMToolchain}
			component.BuildCommand = nil
			component.BuildNetwork = ir.NetworkDefault
			component.Packer = &catalog.PackerConfig{
				ProjectPath: ".", PackerVersion: "1.15.4", BuildName: "image.qemu.node",
				ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
				MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
			}
			if row.host == execution.HostPlatformDarwinARM64 {
				component.BuildRequirements.Capabilities = []execution.CapabilityRequirement{{Name: execution.CapabilityHVF, Policy: execution.CapabilityRequired}}
			}
			resolved := &Resolved{Platform: "linux/arm64", Components: []catalog.NamedComponent{{Name: "image", Component: component}}}
			graph, err := CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{"image": row.host})
			if code(err) != row.code {
				t.Fatalf("compilation = %v; want %s", err, row.code)
			}
			if err != nil {
				if graph != nil {
					t.Fatal("rejected selection returned a partial graph")
				}
				return
			}
			var fetch, build ir.Node
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindFetch {
					fetch = node
				}
				if node.Kind == ir.KindBuild {
					build = node
				}
			}
			if fetch.VendorImage != testVMToolchain || build.Toolchain != row.want || build.ToolchainPlatform != string(row.host) {
				t.Fatalf("vendoring/execution conflated: fetch=%+v build=%+v", fetch, build)
			}
		})
	}
}
