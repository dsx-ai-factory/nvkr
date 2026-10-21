// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"os"
	"path/filepath"
	"slices"
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
		{"../../../inputs/catalogs/nvke/release", "nvke-controller-chart", "charts/nvke-controller-0.22.0.tgz"},
		{"../../../inputs/catalogs/nvke/local-site-arm64", "nvke-controller-chart", "charts/nvke-controller-0.22.0.tgz"},
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
	const inputRoot = "../../../inputs/definitions/nvke"
	const selectedSource = "400c24fb6465b042be883d6b62472528520d6291"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(inputRoot, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct{ platform, root, variant, encryption string }{
		"nvke-node-fde-amd64":   {"linux/amd64", "nvke-node-fde-amd64", "fde", "luks2"},
		"nvke-node-fde-arm64":   {"linux/arm64", "nvke-node-fde-arm64", "fde", "luks2"},
		"nvke-node-image-amd64": {"linux/amd64", "nvke-node-image-amd64", "non-fde", "none"},
		"nvke-node-image-arm64": {"linux/arm64", "nvke-node-image", "non-fde", "none"},
	}
	seen := make(map[string]bool)
	for _, output := range authored.Outputs {
		if output.Format != definition.OutputDiskImage {
			continue
		}
		want, ok := expected[output.Name]
		if !ok || output.Platform != want.platform {
			t.Fatalf("unexpected native disk output %s/%s", output.Name, output.Platform)
		}
		if seen[output.Name] {
			t.Fatalf("duplicate native disk output %s", output.Name)
		}
		seen[output.Name] = true
		t.Run(output.Name, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(inputRoot, output.Catalog.Base)))
			if err != nil {
				t.Fatal(err)
			}
			agent := cat.Components["nvke-agent"]
			for _, name := range []string{"nvke-agent", "nvke-cni-calico", "nvke-kubeadm-distro"} {
				component := cat.Components[name]
				if component.Tag != "v0.22.0" || component.Commit != selectedSource || component.TreeHash != "f3afc02f5c92f4aacda3a1655836a91bef093bfe" || component.SourceDate != 1792548398 {
					t.Fatalf("%s does not retain the selected source commit/commit/tree/epoch", name)
				}
			}
			// SPIRE packaging is part of the same release source identity.
			spire := cat.Components["nvke-spire-agent"]
			if spire.Tag != "v0.22.0" || spire.Commit != selectedSource || spire.TreeHash != "f3afc02f5c92f4aacda3a1655836a91bef093bfe" || spire.SourceDate != 1792548398 {
				t.Fatal("SPIRE source must match the release")
			}
			if len(spire.Materials) != 0 || !strings.Contains(strings.Join(spire.VendorCommand, "\n"), "make -C os-image/packages/spire-agent bundle nfpm") ||
				!strings.Contains(strings.Join(spire.BuildCommand, "\n"), "SPIRE_VERSION=1.14.1 TPM_PLUGIN_VERSION=1.9.0 DEB_REVISION=4") {
				t.Fatal("release SPIRE producer must retain its source-owned acquisition and packaging revision 4 without DSX materials")
			}
			if _, exists := cat.Components["dsx-imds-node-attestor-agent"]; exists || slices.Contains(output.Components, "dsx-imds-node-attestor-agent") {
				t.Fatal("release SPIRE selection unexpectedly reintroduced DSX")
			}
			if agent.VersionStamp["BUILD_VERSION"] != "v0.22.0" || !strings.Contains(strings.Join(agent.BuildCommand, "\n"), `VERSION="$BUILD_VERSION"`) {
				t.Fatal("agent version must derive from the selected source tag")
			}
			selected, err := Resolve(cat, Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
			if err != nil {
				t.Fatal(err)
			}
			root, err := DiskRootName(selected.Components)
			if err != nil || root != want.root {
				t.Fatalf("selected disk root = %q, want %q: %v", root, want.root, err)
			}
			graph, err := CompileForHostPlatforms(selected, map[string]execution.HostPlatform{root: execution.HostPlatform(output.Platform)})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			compositionFound := false
			spireFound := false
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindBuild && node.Component == "nvke-spire-agent" {
					spireFound = true
					if node.SourceCommit != spire.Commit || node.Network != ir.NetworkNone || len(node.Materials) != 0 {
						t.Fatal("SPIRE packaging must use the exact temporary source and its acquired closure offline")
					}
				}
				if node.Kind == ir.KindBuild && node.Component == "nvke-image-inputs" {
					compositionFound = true
					arch := strings.TrimPrefix(output.Platform, "linux/")
					if node.SourceCommit != selectedSource || !strings.Contains(strings.Join(node.Command, "\n"), "--architecture "+arch) {
						t.Fatal("image composition must use the selected source's required native architecture argument")
					}
					spireMaterial := false
					for _, binding := range node.Materials {
						if binding.Component == "nvke-spire-agent" && slices.Contains(node.Needs, binding.Producer) {
							spireMaterial = true
						}
					}
					if !spireMaterial {
						t.Fatal("image composition must consume the selected SPIRE package")
					}
				}
				if node.Kind != ir.KindBuild || node.Component != root {
					continue
				}
				found = true
				if node.Platform != output.Platform || node.ToolchainPlatform != output.Platform || node.SourceCommit != selectedSource {
					t.Fatalf("native source/toolchain binding changed: %+v", node)
				}
				if node.Packer == nil || node.Packer.AcquisitionArchive != want.variant+"/apt-acquisitions.tar" {
					t.Fatal("capture carrier did not reach compiled graph")
				}
				if len(node.Patches) != 1 || node.Patches[0].Name != "patches/capture-schema.patch" ||
					!strings.Contains(node.Patches[0].Content, `+SCHEMA = "nvkr.dev/package-acquisitions/v1"`) {
					t.Fatal("capture producer must use the same schema as its consumer through an authenticated source patch")
				}
				if node.ArtifactPath != want.variant+"/nvke-node.qcow2" || node.Disk == nil || node.Disk.Encryption != want.encryption ||
					node.Packer.BuildName != "nvke-node-"+want.variant+".qemu.nvke-"+want.variant {
					t.Fatal("selected disk variant did not reach the source target and output paths")
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
				if node.Packer.MaterialManifest == nil || node.Packer.MaterialManifest.SchemaVersion != "nvke.nvidia.com/os-image-materials/v1" {
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
			if !spireFound {
				t.Fatal("disk graph must retain the selected SPIRE packaging producer")
			}
		})
	}
	if len(seen) != len(expected) {
		t.Fatal("both FDE and non-FDE capture graphs are required on each native architecture")
	}
}
