// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeToolSelectionKeepsExplicitOSBundles(t *testing.T) {
	for _, tc := range []struct{ platform, bundle string }{
		{"darwin/arm64", "native-tools-darwin-arm64"}, {"linux/arm64", "native-tools-linux-arm64"},
		{"linux/amd64", ""}, {"windows/arm64", ""}, {"", ""},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			definition, environment, bundle, members, err := nativeToolSelection(tc.platform)
			if tc.bundle == "" {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || definition != "" || environment != "" || members != nil {
					t.Fatalf("unsupported host selected outputs: %v", err)
				}
				return
			}
			if err != nil || bundle != tc.bundle || !strings.HasSuffix(definition, ".json") || environment == "" || len(members) == 0 {
				t.Fatalf("selection %q, %q, %v", definition, bundle, err)
			}
			if tc.platform == "darwin/arm64" {
				if definition != "inputs/definitions/demo/native-mac/definition-tools.json" || len(members) != 12 || !slices.Contains(members, "nvke-installer-sources") || !slices.Contains(members, "nerdctl") {
					t.Fatalf("Mac dependency preparation omitted source tools: %s %v", definition, members)
				}
			}
		})
	}
}

func TestNativeToolMembershipIncludesInstallerArchive(t *testing.T) {
	_, _, _, members, err := nativeToolSelection("darwin/arm64")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"complete", "missing installer", "unexpected file", "executable installer", "nonexecutable command", "symlink installer"} {
		t.Run(name, func(t *testing.T) {
			directory := privateTestDir(t)
			for _, member := range members {
				if name == "missing installer" && member == "nvke-installer-sources" {
					continue
				}
				mode := os.FileMode(0700)
				if member == "nvke-installer-sources" {
					mode = 0644
					if name == "executable installer" {
						mode = 0755
					}
					if name == "symlink installer" {
						if err := os.Symlink("nvkectl", filepath.Join(directory, member)); err != nil {
							t.Fatal(err)
						}
						continue
					}
				} else if name == "nonexecutable command" && member == "nvkectl" {
					mode = 0644
				}
				if err := os.WriteFile(filepath.Join(directory, member), []byte("fixture"), mode); err != nil {
					t.Fatal(err)
				}
			}
			if name == "unexpected file" {
				if err := os.WriteFile(filepath.Join(directory, "unexpected"), []byte("fixture"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyToolMembers(directory, members)
			if (err == nil) != (name == "complete") {
				t.Fatalf("membership check: %v", err)
			}
		})
	}
}

func TestToolPreparationRejectsBeforeWorkspaceWork(t *testing.T) {
	for _, tc := range []struct {
		name, platform, attempt string
		jobs                    int
		code                    nvkrerr.Code
	}{
		{"unsupported", "windows/arm64", "test", 1, nvkrerr.CodeEngineCapability},
		{"empty attempt", "darwin/arm64", "", 1, nvkrerr.CodeUsage},
		{"unsafe attempt", "darwin/arm64", "../test", 1, nvkrerr.CodeUsage},
		{"jobs", "darwin/arm64", "test", 0, nvkrerr.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := prepareTools(t.Context(), ToolConfig{Attempt: tc.attempt, Jobs: tc.jobs, Repository: "absent", Out: "absent"}, tc.platform)
			var coded *nvkrerr.Error
			if out != "" || !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("admission %q, %v", out, err)
			}
		})
	}
}

func TestSiteStartupToolsAreSeparateFromHostExecutables(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "linux/arm64", "windows/arm64"} {
		t.Run(platform, func(t *testing.T) {
			bundles := siteStartupToolBundles(platform)
			if platform != "darwin/arm64" && platform != "linux/arm64" {
				if len(bundles) != 0 {
					t.Fatal("other hosts unexpectedly selected Mac site-startup companions")
				}
				return
			}
			if len(bundles) != 1 || bundles[0].name != "site-startup-tools-arm64" || bundles[0].directory != "bin" {
				t.Fatal("guest preparation must select only its runtime bundle")
			}
			if !slices.Contains(bundles[0].members, "nerdctl") || slices.Contains(bundles[0].members, "nvkectl") || slices.Contains(bundles[0].members, "kind") {
				t.Fatal("guest dependencies must contain its runtime and exclude the host CLI")
			}
		})
	}
}

// Split selections describe the same outputs as the complete tools selection;
// they do not introduce another CLI or rebuild unused Linux installer tools.
func TestNativeToolDefinitionsShareOutputOwners(t *testing.T) {
	for _, host := range []string{"native-linux", "native-mac"} {
		t.Run(host, func(t *testing.T) {
			base := filepath.Join("..", "..", "inputs", "definitions", "demo", host)
			complete, err := definition.LoadDefinition(t.Context(), filepath.Join(base, "definition-tools.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(complete.Outputs) != 2 {
				t.Fatalf("unexpected dependency bundles: %v", complete.Outputs)
			}
			if complete.Catalog.Base != "../../../catalogs/tools/native" {
				t.Fatal("tools lost their shared catalog owner")
			}
			outputs := make(map[string]definition.DefinitionOutput)
			for _, output := range complete.Outputs {
				if _, exists := outputs[output.Name]; exists {
					t.Fatal("duplicate tool bundle", output.Name)
				}
				outputs[output.Name] = output
			}
			for _, file := range []string{"definition-host-tools.json", "definition-site-tools.json"} {
				selected, err := definition.LoadDefinition(t.Context(), filepath.Join(base, file))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(selected.Catalog, complete.Catalog) || len(selected.Outputs) != 1 {
					t.Fatal("split selection introduced a different owner or extra bundle")
				}
				output := selected.Outputs[0]
				if !reflect.DeepEqual(output, outputs[output.Name]) {
					t.Fatal("split selection differs from complete selection", output.Name)
				}
				delete(outputs, output.Name)
			}
			if len(outputs) != 0 {
				t.Fatal("complete selection has unused bundles", outputs)
			}
		})
	}
}
