// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"sigs.k8s.io/yaml"
)

func TestPrepareNativePackerUsesTheExecutionResolverBeforePublishing(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, osName, purpose, install string
		corrupt                        bool
		want                           e.Code
	}{
		{"native", runtime.GOOS, "packer-toolchain", "/usr/local/bin/packer", false, ""},
		{"wrong host", "foreign", "packer-toolchain", "/usr/local/bin/packer", false, e.CodeEngineCapability},
		{"wrong purpose", runtime.GOOS, "operator-tools", "/usr/local/bin/packer", false, e.CodeEngineCapability},
		{"missing program", runtime.GOOS, "packer-toolchain", "/usr/local/bin/other", false, e.CodeReleaseIncomplete},
		{"unknown role", runtime.GOOS, "packer-toolchain", "/etc/other", false, e.CodeInvalidCatalog},
		{"not executable bytes", runtime.GOOS, "packer-toolchain", "/usr/local/bin/packer", true, e.CodeEngineCapability},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			program := executable
			if row.corrupt {
				program = filepath.Join(t.TempDir(), "invalid")
				if err := os.WriteFile(program, []byte("invalid executable"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			request := delivery.BinaryBundleRequest{OS: row.osName, Arch: runtime.GOARCH, Purpose: row.purpose,
				Files: []delivery.BinaryFileInput{{Name: "packer", Path: program, InstallPath: row.install, Mode: 0o755}}}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(t.TempDir(), "files.json")
			if err := os.WriteFile(manifest, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			const destination = ".nvkr/packer-dependencies"
			prepared, err := PrepareNativePacker(t.Context(), manifest, destination)
			if factoryErrorCode(err) != row.want {
				t.Fatalf("preparation = %v, want %s", err, row.want)
			}
			if row.want != "" {
				if _, err := os.Lstat(".nvkr"); !os.IsNotExist(err) {
					t.Fatal("rejected preparation created output")
				}
				return
			}
			toolchain, err := openNativePackerBundle(t.Context(), prepared.Layout, prepared.Bundle, prepared.Toolchain, prepared.Platform)
			if err != nil {
				t.Fatal(err)
			}
			if err := toolchain.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareNativePacker(t.Context(), manifest, destination); factoryErrorCode(err) != e.CodeUsage {
				t.Fatalf("existing dependency layout replaced: %v", err)
			}
		})
	}
}

func TestPrepareNativePackerKernelOverrideSurvivesCapturedPlan(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac ARM64 input preparation")
	}
	root, _, _ := snapshotFixture(t)
	t.Chdir(root)
	definition, environment, vm := vmFactoryInputs(t, "linux/arm64", execution.CapabilityRequired)
	component := vm.Catalog.Components["image"]
	component.Platforms = []string{"linux/arm64"}
	component.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatformLinuxARM64}
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.15.4", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
		Variables: map[string]string{"cpu_model": "host", "kernel_package": ""},
	}
	raw, err := os.ReadFile(filepath.Join(root, "catalog", catalog.RegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err := yaml.UnmarshalStrict(raw, &registry); err != nil {
		t.Fatal(err)
	}
	// Author a disk component using an existing fixture source pin. Do not change
	// an existing component's output contract or manufacture a new source lock.
	registry["components"].(map[string]any)["image"] = map[string]any{
		"source": "containerd", "fromSource": true, "platforms": component.Platforms,
		"builderType": component.BuilderType, "outputType": component.OutputType,
		"disk": component.Disk, "packer": component.Packer, "artifactPath": component.ArtifactPath,
		"buildRequirements": component.BuildRequirements,
	}
	registry["sources"].(map[string]any)["containerd"].(map[string]any)["toolchainImagesByArch"] = component.ToolchainImages
	writeSnapshotFixtureDocument(t, root, "catalog/registry.yaml", registry)
	definition.Catalog = authored.Catalog{Base: "../catalog"}
	environment.ExecutionClasses = append(environment.ExecutionClasses, execution.Class{
		Name: "mac-hvf", RunsOn: "local-mac-arm64", HostPlatform: execution.HostPlatformDarwinARM64,
		Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
		Capabilities: []execution.CapabilityName{execution.CapabilityHVF}, MaxTimeoutSeconds: 10800,
	})
	environment.ExecutionClasses = append(environment.ExecutionClasses, execution.Class{
		Name: "mac-tcg", RunsOn: "local-mac-tcg", HostPlatform: execution.HostPlatformDarwinARM64,
		Resources: execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536}, MaxTimeoutSeconds: 10800,
	})
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	original, err := loadFactoryCatalogStack(t.Context(), snapshotDefinition, definition.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	layout, digest := nativePackerBundleFixture(t, "packer-toolchain", runtime.GOOS, runtime.GOARCH)
	var defaultBuild string
	for _, row := range []struct{ name, kernel, accelerator, cpu string }{
		{"source-default", "", "hvf", "host"},
		{"apple-hvf", "linux-generic", "hvf", "host"},
		{"source-tcg", "", "tcg", "max"},
	} {
		t.Run(row.name, func(t *testing.T) {
			variables := map[string]string{"cpu_model": row.cpu}
			if row.kernel != "" {
				variables["kernel_package"] = row.kernel
			}
			prepared, err := PrepareNativePackerBuildWithAccelerator(t.Context(), snapshotDefinition, snapshotEnvironment, layout, "packer-tools", digest,
				filepath.Join(".nvkr", row.name), row.accelerator, variables)
			if err != nil {
				t.Fatal(err)
			}
			captured, snapshot, err := CaptureCompileFiles(t.Context(), root, prepared.Definition, prepared.Environment, "kernel-test")
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := CompileSnapshot(t.Context(), snapshot, "kernel-test")
			if err != nil {
				t.Fatal(err)
			}
			capturedDigest, err := captured.Digest()
			if err != nil {
				t.Fatal(err)
			}
			replayedDigest, err := replayed.Digest()
			if err != nil || replayedDigest != capturedDigest {
				t.Fatalf("captured kernel Plan changed during replay: %v", err)
			}
			for _, plan := range []*Plan{captured, replayed} {
				seen := 0
				for _, node := range plan.Builds[0].Graph.Nodes {
					if node.Packer == nil {
						continue
					}
					seen++
					found := false
					for _, variable := range node.Packer.Variables {
						if variable.Name == "kernel_package" {
							found = variable.Value == row.kernel
						}
					}
					if !found || node.ExecutionBinding == nil || node.ExecutionBinding.Class.HostPlatform != execution.HostPlatformDarwinARM64 {
						t.Fatalf("captured native image lost kernel %q or Mac execution binding", row.kernel)
					}
					if row.accelerator == "tcg" && (node.ExecutionBinding.Class.Name != "mac-tcg" || len(node.ExecutionBinding.Class.Capabilities) != 0) {
						t.Fatal("explicit TCG preparation selected a hardware-accelerated execution class")
					}
					if node.SourceCommit != original.Catalog.Components["image"].Commit || node.SourceTreeHash != original.Catalog.Components["image"].TreeHash {
						t.Fatal("kernel preparation changed the pinned image source")
					}
				}
				if seen != 1 {
					t.Fatalf("captured Packer producers = %d, want 1", seen)
				}
			}
			if row.name == "source-default" {
				defaultBuild = captured.Builds[0].BuildDigest
			} else if captured.Builds[0].BuildDigest == defaultBuild {
				t.Fatal("explicit kernel override did not change image build identity")
			}
		})
	}
	if _, err := PrepareNativePackerBuildWithAccelerator(t.Context(), snapshotDefinition, snapshotEnvironment, layout, "packer-tools", digest,
		".nvkr/unsupported", "kvm", nil); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("unsupported native Mac accelerator admitted: %v", err)
	}
	if _, err := os.Lstat(".nvkr/unsupported"); !os.IsNotExist(err) {
		t.Fatal("unsupported accelerator created preparation output")
	}
	// Preparation emits overlays; it must not change the source-selected Linux kernel.
	unchanged, err := loadFactoryCatalogStack(t.Context(), snapshotDefinition, definition.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchanged.Catalog.Components["image"], original.Catalog.Components["image"]) {
		t.Fatal("Mac kernel preparation changed the original Linux source or material selection")
	}
}

func TestPrepareNativePackerBuildPreservesSelectedSourcesAndMaterials(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac ARM64 input preparation")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "inputs"), os.DirFS(filepath.Join(repo, "inputs"))); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(root, "inputs/definitions/demo/arm64-upstream-tcg.json")
	environmentPath := filepath.Join(root, "inputs/environments/local-native-mac.json")
	original, err := authored.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	layout, digest := nativePackerBundleFixture(t, "packer-toolchain", runtime.GOOS, runtime.GOARCH)
	prepared, err := PrepareNativePackerBuild(t.Context(), definitionPath, environmentPath, layout, "packer-tools", digest,
		".nvkr/prepared", map[string]string{"cpu_model": "host"})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := authored.LoadDefinition(t.Context(), prepared.Definition)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := authored.LoadEnvironment(t.Context(), prepared.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Outputs) != len(original.Outputs) {
		t.Fatal("native preparation changed the required outputs")
	}
	for i, output := range original.Outputs {
		derived := definition.Outputs[i]
		if derived.Name != output.Name || derived.Platform != output.Platform || derived.Format != output.Format || !reflect.DeepEqual(derived.Components, output.Components) {
			t.Fatalf("native preparation changed the selected output: %q", output.Name)
		}
		selection := original.Catalog
		if output.Catalog != nil {
			selection = *output.Catalog
		}
		before, err := loadFactoryCatalogStack(t.Context(), definitionPath, selection)
		if err != nil {
			t.Fatal(err)
		}
		after, err := loadFactoryCatalogStack(t.Context(), prepared.Definition, *derived.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		for name, old := range before.Catalog.Components {
			got := after.Catalog.Components[name]
			if old.Repo != got.Repo || old.Commit != got.Commit || old.TreeHash != got.TreeHash || old.SourceDate != got.SourceDate || !reflect.DeepEqual(old.Materials, got.Materials) || !reflect.DeepEqual(old.VendorCommand, got.VendorCommand) || !reflect.DeepEqual(old.ToolchainImages, got.ToolchainImages) {
				t.Fatalf("source/material identity changed: %s/%s", output.Name, name)
			}
		}
	}
	plan, err := CompileDocuments(t.Context(), prepared.Definition, definition, prepared.Environment, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if node.BuilderType != ir.BuilderVMImage {
				continue
			}
			seen++
			if node.Toolchain != digest || node.ToolchainPlatform != "darwin/arm64" || node.ExecutionBinding == nil {
				t.Fatal("lost native Packer binding")
			}
		}
	}
	if seen != 1 {
		t.Fatalf("native Packer producers = %d, want 1", seen)
	}
	_, err = PrepareNativePackerBuild(t.Context(), definitionPath, environmentPath, layout, "packer-tools", digest,
		".nvkr/rejected", map[string]string{"undeclared_variable": "value"})
	if factoryErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("undeclared override admitted: %v", err)
	}
	if _, err := os.Lstat(".nvkr/rejected"); !os.IsNotExist(err) {
		t.Fatal("rejected override created output")
	}
}
