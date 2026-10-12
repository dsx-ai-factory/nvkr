// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"go.yaml.in/yaml/v3"
)

// These maintained-catalog checks preserve exact pinned upstream filenames.
// They are not framework requirements for customer catalogs or component names.
func TestNVKEControllerPinnedChartFilename(t *testing.T) {
	for _, tc := range []struct{ catalog, component, artifact string }{
		{"../../../definitions/nvke-controller/catalog", "nvke-controller-chart", "charts/nke-controller-0.1.1.tgz"},
		{"../../../definitions/nvke-local-dev-arm64/site-catalog", "nvke-controller-chart", "charts/nke-controller-0.0.0.tgz"},
	} {
		t.Run(tc.catalog, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(tc.catalog, "registry.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var declarations struct {
				Components map[string]struct {
					ArtifactPath string `yaml:"artifactPath"`
				} `yaml:"components"`
			}
			if err := yaml.Unmarshal(raw, &declarations); err != nil {
				t.Fatal(err)
			}
			component, ok := declarations.Components[tc.component]
			if !ok || component.ArtifactPath != tc.artifact {
				t.Fatalf("component %q must retain pinned upstream artifact %q, got %q", tc.component, tc.artifact, component.ArtifactPath)
			}
		})
	}
}

func TestMaintainedM2NativeCaptureGraph(t *testing.T) {
	const inputRoot = "../../../definitions/nvke-m2"
	const approvedSource = "ff21ba8c07cafff74b68e238a8876af11e5beded"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(inputRoot, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, output := range authored.Outputs {
		if output.Format != definition.OutputDiskImage {
			continue
		}
		if output.Platform != "linux/amd64" && output.Platform != "linux/arm64" {
			t.Fatalf("unsupported native disk platform %s", output.Platform)
		}
		if seen[output.Platform] {
			t.Fatalf("duplicate native disk platform %s", output.Platform)
		}
		seen[output.Platform] = true
		t.Run(output.Name, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(inputRoot, output.Catalog.Base)))
			if err != nil {
				t.Fatal(err)
			}
			agent := cat.Components["nvke-agent"]
			for _, name := range []string{"nvke-agent", "nvke-cni-calico", "nvke-kubeadm-distro"} {
				component := cat.Components[name]
				if component.Commit != approvedSource || component.TreeHash != "b915345debd33ccd63dfbf8146415bf20e94c4ea" || component.SourceDate != 1791257393 {
					t.Fatalf("%s does not retain the approved source commit/tree/epoch", name)
				}
			}
			// Temporary SPIRE-only rollback avoids the inaccessible DSX IMDS asset.
			spire := cat.Components["nvke-spire-agent"]
			if spire.Commit != "726f76944404e1106626b8333d3576bdf67b575b" || spire.TreeHash != "3d6ea77f85d5da2648038425f805a598e23464ba" || spire.SourceDate != 1790192178 {
				t.Fatal("temporary SPIRE source pin changed")
			}
			if agent.Commit != approvedSource || !strings.Contains(strings.Join(agent.BuildCommand, "\n"), "VERSION=v0.2.1-81-gff21ba8") {
				t.Fatal("handwritten agent version no longer matches its exact source pin; revalidate owning source version before repinning")
			}
			selected, err := Resolve(cat, Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
			if err != nil {
				t.Fatal(err)
			}
			root := "nvke-node-image"
			if output.Platform == "linux/amd64" {
				root += "-amd64"
			}
			graph, err := CompileForHostPlatforms(selected, map[string]execution.HostPlatform{root: execution.HostPlatform(output.Platform)})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			compositionFound := false
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindBuild && node.Component == "nvke-image-inputs" {
					compositionFound = true
					arch := strings.TrimPrefix(output.Platform, "linux/")
					if node.SourceCommit != approvedSource || !strings.Contains(strings.Join(node.Command, "\n"), "--architecture "+arch) {
						t.Fatal("image composition must use the approved source's required native architecture argument")
					}
				}
				if node.Kind != ir.KindBuild || node.Component != root {
					continue
				}
				found = true
				if node.Platform != output.Platform || node.ToolchainPlatform != output.Platform || node.SourceCommit != approvedSource {
					t.Fatalf("native source/toolchain binding changed: %+v", node)
				}
				if node.Packer == nil || node.Packer.AcquisitionArchive != "non-fde/apt-acquisitions.tar" {
					t.Fatal("capture carrier did not reach compiled graph")
				}
				capture := false
				for _, variable := range node.Packer.Variables {
					if variable.Name == "capture_apt_acquisitions" && variable.Value == "true" {
						capture = true
					}
				}
				if !capture {
					t.Fatal("source capture opt-in did not reach Packer")
				}
				if node.Packer.MaterialManifest == nil || node.Packer.MaterialManifest.SchemaVersion != "nke.dev/os-image-materials/v1" {
					t.Fatal("source material projection is absent")
				}
				kinds := node.Packer.MaterialManifest.Kinds
				if len(kinds) != 2 || kinds["image-inputs"] != "archive" || kinds["installer-iso"] != "disk-image" {
					t.Fatalf("source material kinds changed: %+v", kinds)
				}
				if len(node.Materials) != 2 {
					t.Fatalf("disk material bindings changed: %+v", node.Materials)
				}
				for _, binding := range node.Materials {
					if _, ok := kinds[binding.Binding]; !ok {
						t.Fatalf("unprojected disk material %+v", binding)
					}
					matched := false
					for _, producer := range graph.Nodes {
						if producer.ID == binding.Producer {
							matched = true
							if producer.Component != binding.Component || producer.Platform != output.Platform {
								t.Fatalf("material is not bound to its native producer: %+v / %+v", binding, producer)
							}
						}
					}
					if !matched {
						t.Fatalf("material producer is absent: %+v", binding)
					}
				}
			}
			if !found {
				t.Fatal("disk producer missing from graph")
			}
			if !compositionFound {
				t.Fatal("image-input composition producer missing from graph")
			}
		})
	}
	if len(seen) != 2 || !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Fatal("both native disk capture graphs are required")
	}
}
