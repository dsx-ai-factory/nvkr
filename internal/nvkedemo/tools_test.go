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

// The complete selection is the authored owner of both tool bundles.
func TestNativeToolDefinitionsMatchHostAndSiteSelections(t *testing.T) {
	for _, platform := range []string{"linux/arm64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			path, _, hostBundle, members, err := nativeToolSelection(platform)
			if err != nil {
				t.Fatal(err)
			}
			complete, err := definition.LoadDefinition(t.Context(), filepath.Join("..", "..", path))
			if err != nil {
				t.Fatal(err)
			}
			if len(complete.Outputs) != 2 {
				t.Fatalf("unexpected dependency bundles: %v", complete.Outputs)
			}
			if complete.Catalog.Base != "../../../catalogs/tools/native" {
				t.Fatal("tools lost their shared catalog owner")
			}
			expected := map[string]definition.DefinitionOutput{
				hostBundle:                 {Name: hostBundle, Platform: platform, Format: definition.OutputExecutable, Components: slices.Clone(members)},
				"site-startup-tools-arm64": {Name: "site-startup-tools-arm64", Platform: "linux/arm64", Format: definition.OutputExecutable, Components: []string{"nerdctl"}},
			}
			if platform == "linux/arm64" {
				for i, member := range expected[hostBundle].Components {
					if slices.Contains([]string{"clusterctl", "helm", "kind", "kubectl", "oras"}, member) {
						expected[hostBundle].Components[i] += "-linux"
					}
				}
			}
			for _, output := range complete.Outputs {
				if !reflect.DeepEqual(output, expected[output.Name]) {
					t.Fatal("tool bundle differs from selected host or site members", output.Name)
				}
				delete(expected, output.Name)
			}
			if len(expected) != 0 {
				t.Fatal("complete selection omitted required bundles", expected)
			}
		})
	}
}
