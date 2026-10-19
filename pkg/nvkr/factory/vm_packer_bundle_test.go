// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func nativePackerBundleFixture(t *testing.T, purpose, osName, arch string, extra ...delivery.BinaryFileInput) (string, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bundleRoot := filepath.Join(root, "bundle")
	bundle, err := delivery.BuildBinaryBundle(t.Context(), bundleRoot, delivery.BinaryBundleRequest{
		OS: osName, Arch: arch, Purpose: purpose,
		Files: append([]delivery.BinaryFileInput{{Name: "packer", Path: executable, InstallPath: "/usr/local/bin/packer", Mode: 0o755}}, extra...),
	})
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(root, "layout")
	_, err = delivery.BuildPublicationRoot(t.Context(), layout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{
		Name: "packer-tools", Kind: "binary-bundle", Path: bundleRoot, Layout: true,
		ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, OS: osName, Arch: arch,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return layout, bundle.SemanticDigest
}

func TestNativePackerBundleCapturesFirmwareWithoutSourcePathIdentity(t *testing.T) {
	const logical = pipeline.NativePackerFilePrefix + "firmware/code.fd"
	makeBundle := func(data string) (string, string) {
		file := filepath.Join(t.TempDir(), "code.fd")
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return nativePackerBundleFixture(t, "packer-toolchain", runtime.GOOS, runtime.GOARCH,
			delivery.BinaryFileInput{Name: "firmware", Path: file, InstallPath: logical, Mode: 0o644})
	}
	layout, digest := makeBundle("firmware bytes")
	_, relocated := makeBundle("firmware bytes")
	_, changed := makeBundle("different firmware")
	if digest != relocated || digest == changed {
		t.Fatal("toolchain identity must bind firmware bytes and omit their source locator")
	}
	toolchain, err := openNativePackerBundle(t.Context(), layout, "packer-tools", digest, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	variables := []pipeline.PackerVariable{{Name: "firmware", Value: logical}}
	resolved, err := toolchain.NativeVariables(t.Context(), variables)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved[0].Value)
	if err != nil || string(data) != "firmware bytes" || variables[0].Value != logical {
		t.Fatalf("incorrect native firmware binding: %q %v", data, err)
	}
	if err := toolchain.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resolved[0].Value); !os.IsNotExist(err) {
		t.Fatal("firmware was not joined to toolchain cleanup", err)
	}
}

func TestNativePackerBundleBindsExactIdentityPlatformAndPurpose(t *testing.T) {
	for _, row := range []struct {
		name, purpose, osName, arch string
		wrongDigest, wrongName      bool
		want                        e.Code
	}{
		{"exact", "packer-toolchain", runtime.GOOS, runtime.GOARCH, false, false, ""},
		{"wrong purpose", "operator-tools", runtime.GOOS, runtime.GOARCH, false, false, e.CodeUnverifiedRef},
		{"wrong platform", "packer-toolchain", "foreign", runtime.GOARCH, false, false, e.CodeUnverifiedRef},
		{"wrong digest", "packer-toolchain", runtime.GOOS, runtime.GOARCH, true, false, e.CodeUnverifiedRef},
		{"wrong name", "packer-toolchain", runtime.GOOS, runtime.GOARCH, false, true, e.CodeUnverifiedRef},
	} {
		t.Run(row.name, func(t *testing.T) {
			layout, digest := nativePackerBundleFixture(t, row.purpose, row.osName, row.arch)
			name := "packer-tools"
			if row.wrongDigest {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			if row.wrongName {
				name = "other"
			}
			toolchain, err := openNativePackerBundle(t.Context(), layout, name, digest, runtime.GOOS+"/"+runtime.GOARCH)
			if factoryErrorCode(err) != row.want {
				t.Fatalf("bundle admission = %v, want %s", err, row.want)
			}
			if toolchain != nil {
				if err := toolchain.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCompileNativeHVFToolchainAndRejectHostedRendering(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/arm64", execution.CapabilityRequired)
	base := *stack.Catalog
	base.Components = make(map[string]catalog.Component)
	for name, component := range stack.Catalog.Components {
		component.Platforms = []string{"linux/arm64"}
		component.ToolchainImage = "sha256:" + strings.Repeat("d", 64)
		component.ToolchainImages = map[string]string{"arm64": "example.test/vendor@sha256:" + strings.Repeat("e", 64)}
		component.BuildCommand = nil
		component.BuildNetwork = ir.NetworkDefault
		component.Packer = &catalog.PackerConfig{
			ProjectPath: ".", PackerVersion: "1.15.4", BuildName: "image.qemu.node",
			ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
			MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
		}
		component.BuildRequirements = execution.Clone(component.BuildRequirements)
		component.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatformDarwinARM64}
		component.BuildRequirements.Capabilities = []execution.CapabilityRequirement{{Name: execution.CapabilityHVF, Policy: execution.CapabilityRequired}}
		base.Components[name] = component
	}
	nativeStack, err := catalog.ApplyStack(&base)
	if err != nil {
		t.Fatal(err)
	}
	environment.ExecutionClasses = []execution.Class{{
		Name: "native-mac", RunsOn: "local-mac", HostPlatform: execution.HostPlatformDarwinARM64,
		Capabilities: []execution.CapabilityName{execution.CapabilityHVF},
		Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536}, MaxTimeoutSeconds: 10800,
	}}
	environment.Runtime.BuildNetworkPolicy = authored.BuildNetworkProducerDeclared
	environment.Runtime.NativeToolchains = map[string]authored.NativeToolchain{
		"sha256:" + strings.Repeat("d", 64): {Layout: ".nvkr/fixture-layout", Bundle: "packer-tools"},
	}
	plan, err := Compile(t.Context(), definition, environment, nativeStack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if node.BuilderType == ir.BuilderVMImage && node.ToolchainPlatform != "darwin/arm64" {
				t.Fatal("guest architecture replaced the native host binding")
			}
		}
	}
	if _, err := RenderGitHub(t.Context(), plan); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("native bundle admitted to hosted execution: %v", err)
	}
}
