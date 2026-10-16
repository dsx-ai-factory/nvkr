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

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
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

func TestPrepareNativePackerBuildPreservesSelectedSourcesAndMaterials(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac ARM64 input preparation")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(repo, "inputs/definitions/demo/arm64-upstream-tcg.json")
	environmentPath := filepath.Join(repo, "inputs/environments/local-native-mac.json")
	original, err := authored.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
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
