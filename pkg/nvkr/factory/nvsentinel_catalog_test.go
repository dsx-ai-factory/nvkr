// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"sigs.k8s.io/yaml"
)

// Compile the checked-in inputs without source access or execution. These checks
// protect separate experiment copies; they do not establish package acceptance.
func TestNVSentinelCatalogInputs(t *testing.T) {
	t.Chdir("../../..")
	const root = "."
	mainDir := filepath.Join(root, "inputs/catalogs/nvsentinel")
	experimentDir := filepath.Join(root, "inputs/experiments/nvsentinel/catalog")
	mainCatalog, err := catalog.LoadContext(t.Context(), catalog.Dir(mainDir))
	if err != nil {
		t.Fatal(err)
	}
	experimentCatalog, err := catalog.LoadContext(t.Context(), catalog.Dir(experimentDir))
	if err != nil {
		t.Fatal(err)
	}
	experimentDigest, err := experimentCatalog.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, tc := range []struct {
			name, environment string
			experiment        bool
		}{
			{"binaries", "image", false},
			{"images", "image", false},
			{"image", "image", false},
			{"helm", "image", false},
			{"debian-ubuntu-24.04", "ubuntu-24.04", false},
			{"debian-ubuntu-26.04", "ubuntu-26.04", false},
			{"os-ubuntu2404", "os", true},
			{"os-ubuntu2604", "os", true},
		} {
			t.Run(tc.name+"-"+arch, func(t *testing.T) {
				directory := "inputs/definitions/nvsentinel"
				if tc.experiment {
					directory = "inputs/experiments/nvsentinel"
				}
				definitionPath := filepath.Join(root, directory, tc.name+"-"+arch+".yaml")
				environmentPath := filepath.Join(root, "inputs/environments/nvsentinel", tc.environment+"-"+arch+".yaml")
				doc, err := definition.LoadDefinition(t.Context(), definitionPath)
				if err != nil {
					t.Fatal(err)
				}
				env, err := definition.LoadEnvironment(t.Context(), environmentPath)
				if err != nil {
					t.Fatal(err)
				}
				if tc.experiment {
					if len(doc.Catalog.Overlays) != 1 {
						t.Fatal("OS experiment must retain one predecessor-bound overlay")
					}
					overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(filepath.Dir(definitionPath), doc.Catalog.Overlays[0]))
					if err != nil {
						t.Fatal(err)
					}
					if overlay.RequiresCatalogDigest != experimentDigest {
						t.Fatal("OS overlay does not bind the current experiment catalog")
					}
				}
				plan, err := CompileDocuments(t.Context(), definitionPath, doc, environmentPath, env, "nvsentinel-input-contract")
				if err != nil {
					t.Fatal(err)
				}
				if err := plan.Validate(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}

	readDeclarations := func(directory string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(directory, catalog.RegistryFile))
		if err != nil {
			t.Fatal(err)
		}
		var declarations map[string]any
		if err := yaml.UnmarshalStrict(raw, &declarations); err != nil {
			t.Fatal(err)
		}
		return declarations
	}
	mainDeclarations, experimentDeclarations := readDeclarations(mainDir), readDeclarations(experimentDir)
	mainComponents := mainDeclarations["components"].(map[string]any)
	experimentComponents := experimentDeclarations["components"].(map[string]any)
	mainSources := mainDeclarations["sources"].(map[string]any)
	experimentSources := experimentDeclarations["sources"].(map[string]any)
	mainLock, err := catalog.LoadLockContext(t.Context(), mainDir)
	if err != nil {
		t.Fatal(err)
	}
	experimentLock, err := catalog.LoadLockContext(t.Context(), experimentDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ component, source string }{
		{"nvsentinel-platform-connectors-deb-ubuntu2404", "nvsentinel"},
		{"nvsentinel-platform-connectors-deb-ubuntu2604", "nvsentinel-ubuntu2604"},
	} {
		t.Run("experiment-copy/"+tc.component, func(t *testing.T) {
			if mainComponents[tc.component] == nil || !reflect.DeepEqual(mainComponents[tc.component], experimentComponents[tc.component]) {
				t.Fatal("experiment Debian declaration differs from the dedicated catalog")
			}
			if !reflect.DeepEqual(mainCatalog.Components[tc.component], experimentCatalog.Components[tc.component]) {
				t.Fatal("experiment resolved Debian producer differs from the dedicated catalog")
			}
			if !reflect.DeepEqual(mainSources[tc.source], experimentSources[tc.source]) || !reflect.DeepEqual(mainCatalog.Sources[tc.source], experimentCatalog.Sources[tc.source]) || !reflect.DeepEqual(mainLock.Pins[tc.source], experimentLock.Pins[tc.source]) {
				t.Fatal("experiment Debian source, vendor or pin identity differs from the dedicated catalog")
			}
		})
	}

	t.Run("toolchain-routes", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(root, "toolchains/nvsentinel-debian/toolchain.lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		var lock struct {
			Routes []struct{ Distribution, Platform, LocalRepositoryDigest string }
		}
		if err := json.Unmarshal(raw, &lock); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"ubuntu-24.04/linux/amd64": "nvsentinel", "ubuntu-24.04/linux/arm64": "nvsentinel", "ubuntu-26.04/linux/amd64": "nvsentinel-ubuntu2604", "ubuntu-26.04/linux/arm64": "nvsentinel-ubuntu2604"}
		for _, route := range lock.Routes {
			key := route.Distribution + "/" + route.Platform
			source, exists := want[key]
			if !exists {
				t.Fatalf("unexpected or repeated toolchain route %q", key)
			}
			arch := strings.TrimPrefix(route.Platform, "linux/")
			if route.LocalRepositoryDigest == "" || mainCatalog.Sources[source].ToolchainImagesByArch[arch] != route.LocalRepositoryDigest {
				t.Fatalf("toolchain route %q differs from its catalog source image", key)
			}
			delete(want, key)
		}
		if len(want) != 0 {
			t.Fatalf("missing toolchain routes: %v", want)
		}
	})
}
