// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

func mustCompile(t testing.TB, resolved *Resolved) *ir.Graph {
	t.Helper()
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatalf("compile build graph: %v", err)
	}
	return graph
}

func TestCompileNativeMacAcquiredExecutable(t *testing.T) {
	tool := catalog.ToolPin{Format: "tar-gzip", URL: "https://example.test/tool.tar.gz", Platform: "darwin/arm64", ExecutablePath: "bin/tool", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}
	component := catalog.Component{Platforms: []string{tool.Platform}, OutputType: "host-package", Tag: "v1.2.3", SourceDate: 1, Prebuilt: &catalog.Prebuilt{URL: tool.URL, Digests: map[string]string{"arm64": tool.ExecutableDigest}, Executable: &tool, Evidence: &catalog.ExternalEvidence{None: true}}}
	for _, format := range []string{definition.OutputExecutable, definition.OutputOCI, ""} {
		t.Run(format, func(t *testing.T) {
			resolved, err := Resolve(&catalog.Catalog{Components: map[string]catalog.Component{"tool": component}}, Selection{Components: []string{"tool"}, Platform: tool.Platform, OutputFormat: format})
			if format != definition.OutputExecutable {
				if resolved != nil || code(err) != nvkrerr.CodeInvalidCatalog {
					t.Fatalf("unsupported format admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			graph := mustCompile(t, resolved)
			pull := graph.Nodes[0]
			if pull.Kind != ir.KindPull || pull.Platform != tool.Platform || pull.Digest != tool.ExecutableDigest || pull.ExecutableArchive.Digest != tool.ArchiveDigest {
				t.Fatalf("lost native acquisition identity: %+v", pull)
			}
		})
	}
	component.Prebuilt.Executable = nil
	if graph, err := Compile(&Resolved{Platform: tool.Platform, Components: []catalog.NamedComponent{{Name: "tool", Component: component}}}); graph != nil || err == nil {
		t.Fatal("Mac acquisition admitted an unbound tool")
	}
}

func TestCompileZipAcquiredExecutable(t *testing.T) {
	tool := catalog.ToolPin{Format: "zip", URL: "https://example.test/tool.zip", Platform: "darwin/arm64", ExecutablePath: "tool", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}
	component := catalog.Component{Platforms: []string{tool.Platform}, OutputType: "host-package", Tag: "v1.2.3", SourceDate: 1, Prebuilt: &catalog.Prebuilt{URL: tool.URL, Digests: map[string]string{"arm64": tool.ExecutableDigest}, Executable: &tool, Evidence: &catalog.ExternalEvidence{None: true}}}
	resolved, err := Resolve(&catalog.Catalog{Components: map[string]catalog.Component{"tool": component}}, Selection{Components: []string{"tool"}, Platform: tool.Platform, OutputFormat: definition.OutputExecutable})
	if err != nil {
		t.Fatal(err)
	}
	node := mustCompile(t, resolved).Nodes[0]
	if node.ExecutableArchive == nil || node.ExecutableArchive.Format != "zip" || node.ExecutableArchive.Digest != tool.ArchiveDigest || node.Digest != tool.ExecutableDigest {
		t.Fatalf("lost ZIP acquisition binding: %+v", node)
	}
}

func TestCompileMacSourceUsesLinuxToolchainAndMacPayload(t *testing.T) {
	base := catalog.Component{Platforms: []string{"darwin/arm64"}, FromSource: true,
		Repo: "https://example.test/source", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/go@sha256:" + strings.Repeat("c", 64), OutputType: "host-package",
		BuildCommand: []string{"go", "build", "-o", "bin/tool", "./cmd/tool"}, WriteDir: "bin", ArtifactPath: "bin/tool", SourceDate: 1}
	for _, tc := range []struct {
		name            string
		cgo             bool
		builder, output string
		valid           bool
	}{
		{"pure Go", false, catalog.BuilderOCIHermetic, "host-package", true},
		{"assets", false, catalog.BuilderOCIHermetic, "tarball", true},
		{"CGO", true, catalog.BuilderOCIHermetic, "host-package", false},
		{"Nix", false, catalog.BuilderNix, "host-package", false},
		{"OCI", false, catalog.BuilderOCIHermetic, "oci-image", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := base
			component.CGO, component.BuilderType, component.OutputType = tc.cgo, tc.builder, tc.output
			resolved, err := Resolve(&catalog.Catalog{Components: map[string]catalog.Component{"tool": component}}, Selection{Components: []string{"tool"}, Platform: "darwin/arm64", OutputFormat: definition.OutputExecutable})
			if !tc.valid {
				if resolved != nil || err == nil {
					t.Fatal("unsupported Mac build admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			graph := mustCompile(t, resolved)
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindBuild {
					continue
				}
				if node.Platform != "darwin/arm64" || node.ToolchainPlatform != "linux/arm64" {
					t.Fatalf("incorrect payload/toolchain binding: %+v", node)
				}
				for _, env := range node.Env {
					if env.Name == "GOOS" && env.Value != "darwin" || env.Name == "CGO_ENABLED" && env.Value != "0" {
						t.Fatalf("incorrect build environment: %+v", node.Env)
					}
				}
				mutated := node
				mutated.ToolchainPlatform = "darwin/arm64"
				if err := mutated.Validate(); err == nil {
					t.Fatal("Mac payload admitted as OCI execution host")
				}
			}
		})
	}
}

func TestCompileCarriesGenericBuildRequirementsOutsideBuildIdentity(t *testing.T) {
	component := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, Repo: "https://example.test/source", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/toolchain@sha256:" + strings.Repeat("c", 64),
		OutputType:     "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/artifact", SourceDate: 1,
	}
	resolved := func(c catalog.Component) *Resolved {
		return &Resolved{Platform: "linux/amd64",
			Components: []catalog.NamedComponent{{Name: "artifact", Component: c}}}
	}

	without := mustCompile(t, resolved(component))
	component.BuildRequirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	with := mustCompile(t, resolved(component))
	if with.Nodes[0].Requirements == nil || !execution.Equal(with.Nodes[0].Requirements, component.BuildRequirements) {
		t.Fatalf("compiled build node lost generic requirements: %#v", with.Nodes[0])
	}
	withoutDigest, err := without.Digest()
	if err != nil {
		t.Fatal(err)
	}
	withDigest, err := with.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if withDigest != withoutDigest {
		t.Fatalf("operational request changed buildDigest: %s != %s", withDigest, withoutDigest)
	}
}

func TestCompileRendersVendorArchitectureBeforeIdentity(t *testing.T) {
	component := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, Repo: "https://example.test/source", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{
			"amd64": "example.test/toolchain@sha256:" + strings.Repeat("c", 64),
			"arm64": "example.test/toolchain@sha256:" + strings.Repeat("d", 64),
		},
		VendorCommand: []string{"make", "vendor", "ARCH={{.Arch}}"},
		OutputType:    "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/artifact", SourceDate: 1,
	}
	for _, test := range []struct {
		arch string
		want string
	}{
		{arch: "amd64", want: "ARCH=amd64"},
		{arch: "arm64", want: "ARCH=arm64"},
	} {
		t.Run(test.arch, func(t *testing.T) {
			graph := mustCompile(t, &Resolved{Platform: "linux/" + test.arch,
				Components: []catalog.NamedComponent{{Name: "artifact", Component: component}}})
			var fetch ir.Node
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindFetch {
					fetch = node
					break
				}
			}
			if !slices.Equal(fetch.VendorCmd, []string{"make", "vendor", test.want}) {
				t.Fatalf("vendor command = %q, want architecture-rendered argv", fetch.VendorCmd)
			}
			wantDigest, err := ir.VendorSpecIdentity(fetch.VendorCmd, fetch.VendorImage)
			if err != nil {
				t.Fatal(err)
			}
			if fetch.VendorSpecDigest != wantDigest {
				t.Fatalf("vendor digest = %q, want identity of rendered argv %q", fetch.VendorSpecDigest, wantDigest)
			}
			if strings.Contains(fetch.VendorSpecDigest+strings.Join(fetch.VendorCmd, ""), "{{.Arch}}") {
				t.Fatal("compiled vendor closure retained an unresolved architecture token")
			}
		})
	}
}

func TestCompileLowersVMProducersOnlyAfterHostSelection(t *testing.T) {
	validVM := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, BuilderType: catalog.BuilderVMImage, OutputType: "disk-image",
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
			},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
			}},
			TimeoutSeconds: 7200,
		},
		Repo: "https://example.test/nvke", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/vm-builder@sha256:" + strings.Repeat("c", 64),
		ToolchainImages: map[string]string{
			"amd64": "example.test/vm-builder@sha256:" + strings.Repeat("c", 64),
		},
		BuildCommand: []string{"make", "-C", "os-image/packer", "os-image-non-fde", "ARCH={{.Arch}}"},
		ArtifactPath: "os-image/packer/output-non-fde/node-{{.Arch}}.qcow2",
		SourceDate:   1,
	}
	validPacker := validVM
	validPacker.BuildCommand = nil
	validPacker.BuildNetwork = ir.NetworkDefault
	validPacker.ArtifactPath = "node-{{.Arch}}.qcow2"
	validPacker.Packer = &catalog.PackerConfig{
		ProjectPath:              "os-image/packer",
		PackerVersion:            "1.14.2",
		BuildName:                "nke-node-non-fde.qemu.nke-non-fde",
		ArchitectureVariable:     "architecture",
		AcceleratorVariable:      "accelerator",
		MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable:  "output_directory",
	}
	resolved := func(component catalog.Component) *Resolved {
		return &Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{{Name: "image", Component: component}}}
	}
	if graph, err := Compile(resolved(validVM)); graph != nil || code(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("VM graph without selected host = %#v, %v; want %s", graph, err, nvkrerr.CodeEngineCapability)
	}
	if graph, err := CompileForHostPlatforms(resolved(validVM), map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxARM64,
	}); graph != nil || code(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("undeclared VM host = %#v, %v; want %s", graph, err, nvkrerr.CodeEngineCapability)
	}
	if graph, err := CompileForHostPlatforms(resolved(validVM), map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxAMD64, "unselected": execution.HostPlatformLinuxAMD64,
	}); graph != nil || code(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("extra VM host selection = %#v, %v; want %s", graph, err, nvkrerr.CodeEngineCapability)
	}
	scalarOnly := validVM
	scalarOnly.ToolchainImages = nil
	if graph, err := CompileForHostPlatforms(resolved(scalarOnly), map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxAMD64,
	}); graph != nil || code(err) != nvkrerr.CodeUnpinnedSource {
		t.Fatalf("scalar VM toolchain fallback = %#v, %v; want %s", graph, err, nvkrerr.CodeUnpinnedSource)
	}
	withUnrelated := resolved(validVM)
	withUnrelated.Components = append(withUnrelated.Components, catalog.NamedComponent{
		Name: "unrelated",
		Component: catalog.Component{
			Platforms: []string{"linux/amd64"}, FromSource: true,
			Repo: "https://example.test/unrelated", Tag: "v1.0.0",
			Commit: strings.Repeat("d", 40), TreeHash: strings.Repeat("e", 40),
			ToolchainImage: "example.test/go@sha256:" + strings.Repeat("f", 64),
			BuildCommand:   []string{"make"}, WriteDir: "out",
			OutputType: "raw", ArtifactPath: "out/unrelated", SourceDate: 1,
		},
	})
	if graph, err := CompileForHostPlatforms(withUnrelated, map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxAMD64,
	}); graph != nil || code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("disk graph with unrelated producer = %#v, %v; want %s", graph, err, nvkrerr.CodeInvalidCatalog)
	}
	for _, test := range []struct {
		name      string
		component catalog.Component
	}{
		{name: "command", component: validVM},
		{name: "Packer", component: validPacker},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph, err := CompileForHostPlatforms(resolved(test.component), map[string]execution.HostPlatform{
				"image": execution.HostPlatformLinuxAMD64,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(graph.Nodes) != 3 || graph.Nodes[0].BuilderType != ir.BuilderVMImage ||
				graph.Nodes[0].Disk == nil || graph.Nodes[0].Disk.Format != "qcow2" {
				t.Fatalf("VM graph did not retain typed disk producer: %#v", graph.Nodes)
			}
			wantNetwork := ir.NetworkNone
			if test.name == "Packer" {
				wantNetwork = ir.NetworkDefault
			}
			if graph.Nodes[0].Network != wantNetwork {
				t.Fatalf("VM graph network = %q, want %q", graph.Nodes[0].Network, wantNetwork)
			}
		})
	}
}

func TestCompileVMSelectsHostToolchainAndMaterialProducers(t *testing.T) {
	root := testVMImageComponent([]execution.HostPlatform{
		execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
	}, execution.CapabilityPreferred)
	root.Tag = "v1.0.0"
	root.BuildCommand = nil
	root.ToolchainImage = ""
	root.ToolchainImages = map[string]string{
		"amd64": "example.test/vm-amd64@sha256:" + strings.Repeat("1", 64),
		"arm64": "example.test/vm-arm64@sha256:" + strings.Repeat("2", 64),
	}
	root.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable: "output_directory",
		MaterialManifest: &material.Projection{
			SchemaVersion: "nke.dev/os-image-materials/v1", Kinds: map[string]string{"agent-package": "archive"},
		},
	}
	root.Materials = map[string]string{"agent-package": "agent"}
	material := catalog.Component{
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		Repo: "https://example.test/agent", Tag: "v1.0.0", Commit: strings.Repeat("3", 40),
		TreeHash: strings.Repeat("4", 40), ToolchainImage: "example.test/go@sha256:" + strings.Repeat("5", 64),
		OutputType: "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/agent", SourceDate: 1,
	}
	resolved := &Resolved{Platform: "linux/arm64", Components: []catalog.NamedComponent{
		{Name: "agent", Component: material}, {Name: "image", Component: root},
	}}
	graph, err := CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxAMD64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var image ir.Node
	for _, node := range graph.Nodes {
		if node.Component == "image" {
			image = node
		}
	}
	if image.Toolchain != root.ToolchainImages["amd64"] || image.ToolchainPlatform != "linux/amd64" ||
		image.Packer == nil || image.Packer.MaterialManifest == nil ||
		image.Packer.MaterialManifest.SchemaVersion != "nke.dev/os-image-materials/v1" ||
		image.Packer.MaterialManifest.Kinds["agent-package"] != "archive" ||
		len(image.Materials) != 1 || image.Materials[0] != (ir.MaterialBinding{
		Binding: "agent-package", Component: "agent", Producer: "build-agent",
	}) || !slices.Contains(image.Needs, "build-agent") {
		t.Fatalf("VM host toolchain/material lowering = %#v", image)
	}

	sharedToolchain := "example.test/vm@sha256:" + strings.Repeat("6", 64)
	root.ToolchainImages = map[string]string{"amd64": sharedToolchain, "arm64": sharedToolchain}
	resolved.Components[1].Component = root
	amd64Graph, err := CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxAMD64,
	})
	if err != nil {
		t.Fatal(err)
	}
	arm64Graph, err := CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{
		"image": execution.HostPlatformLinuxARM64,
	})
	if err != nil {
		t.Fatal(err)
	}
	amd64Digest, err := amd64Graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	arm64Digest, err := arm64Graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if amd64Digest == arm64Digest {
		t.Fatal("distinct VM execution-host platforms produced the same logical build identity")
	}
}

func TestDirectPackerProducerRequiresVMImageBuilderCapability(t *testing.T) {
	resolved := &Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{{
		Name: "image",
		Component: catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
			FromSource:  true,
			BuilderType: catalog.BuilderVMImage,
			Packer: &catalog.PackerConfig{
				ProjectPath:              "os-image/packer",
				PackerVersion:            "1.14.2",
				BuildName:                "nke-node-non-fde.qemu.nke-non-fde",
				ArchitectureVariable:     "architecture",
				AcceleratorVariable:      "accelerator",
				MaterialManifestVariable: "material_manifest",
				OutputDirectoryVariable:  "output_directory",
			},
		},
	}}}
	builders := RequiredBuilders(resolved)
	if len(builders) != 1 || builders[0] != catalog.BuilderVMImage {
		t.Fatalf("direct Packer producer requires builders %v, want [%s]", builders, catalog.BuilderVMImage)
	}
}

func TestCompileRejectsExcludedProducer(t *testing.T) {
	graph, err := Compile(&Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{{Name: "image", Component: catalog.Component{Platforms: []string{"linux/amd64"}, ExclusionReason: "not built"}}}})
	if graph != nil || code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("excluded producer yielded graph=%v, err=%v", graph, err)
	}
}
