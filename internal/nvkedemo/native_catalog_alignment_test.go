// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Compile the maintained selections through the owning boundary: a modern CLI
// paired with legacy disk packages or a different controller cannot bootstrap.
func TestNativeNVIDIADefinitionsAlignRenamedSourceContracts(t *testing.T) {
	testutil.RequireExtended(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const nvke = "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3"
	const ovn = "8af51273d52dfa07454435d18d37230e582d797a"
	for _, conformance := range []bool{false, true} {
		name := "demo"
		if conformance {
			name = "conformance"
		}
		t.Run(name, func(t *testing.T) {
			dpath, epath, err := LocalArtifactInputsFor("ovn", conformance)
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := factory.CaptureCompileFiles(t.Context(), repo, dpath, epath, "test")
			if err != nil {
				t.Fatal(err)
			}
			expected := 32
			if conformance {
				expected = 36
			}
			if len(plan.LifecycleOutputs) != expected {
				t.Fatalf("outputs=%d, want%d", len(plan.LifecycleOutputs), expected)
			}
			rows := []struct{ output, component, commit string }{
				{"nvke-node-image-arm64", "nvke-agent", nvke},
				{"nvke-node-image-arm64", "nvke-kubeadm-distro", nvke},
				{"nvke-node-image-arm64", "nvke-cni-calico", nvke},
				{"nvke-node-image-arm64", "nvke-node-image", nvke},
				{"nvke-node-image-arm64", "nvke-image-inputs", nvke},
				{"nvke-node-image-arm64", "nvke-spire-agent", "726f76944404e1106626b8333d3576bdf67b575b"},
				{"nvke-node-image-arm64", "nvke-cni-ovn", ovn},
				{"nvke-node-image-arm64", "ovn-kube-ubuntu", ovn},
				{"nvke-node-image-arm64", "ovn-kubernetes-chart", ovn},
				{"controller-chart", "nvke-controller-chart", nvke},
				{"controller-profiles", "nvke-local-profiles", nvke},
				{"cwe", "nvke-controller", nvke},
				{"nvkectl", "nvkectl", nvke},
				{"cluster-api-provider-provisioned-arm64", "cluster-api-provider-provisioned", nvke},
				{"ovn-image-arm64", "ovn-kube-ubuntu", ovn},
				{"ovn-chart-arm64", "ovn-kubernetes-chart", ovn},
			}
			for _, row := range rows {
				matches := 0
				for _, build := range plan.Builds {
					if build.Name != row.output {
						continue
					}
					for _, c := range build.ResolvedComponents {
						if c.Name != row.component {
							continue
						}
						matches++
						if c.Commit != row.commit {
							t.Fatalf("%s/%s source=%s, want %s", row.output, row.component, c.Commit, row.commit)
						}
						if (c.Name == "nvke-node-image" || c.Name == "nvke-local-profiles") && len(c.Patches) != 0 {
							t.Fatalf("%s must consume unchanged upstream source", c.Name)
						}
						if c.Name == "nvke-node-image" {
							if c.Packer == nil || c.Packer.BuildName != "nvke-node-non-fde.qemu.nvke-non-fde" || c.ArtifactPath != "non-fde/nvke-node.qcow2" {
								t.Fatal("disk producer does not match renamed Packer source")
							}
						}
					}
				}
				if matches != 1 {
					t.Fatalf("%s/%s matched %d times", row.output, row.component, matches)
				}
			}
		})
	}
}

// Read only authored selections and lock records. Full compilation also
// authenticates private derived-chart pins and belongs to the extended suite.
func TestNativeNVIDIASelectionsAndPinsOffline(t *testing.T) {
	const nvke = "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3"
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dir     string
		sources []string
	}{
		{"node-images/native-arm64", []string{"nvke-os", "nvke-cni-calico-source", "nvke-kubeadm-distro-source"}},
		{"nvke/native-site-arm64", []string{"nvke-cli", "nvke-controller", "nvke-controller-helm", "nvke-nktp-helm"}},
		{"controller-profiles/native-nvidia-arm64", []string{"nvke-profiles"}},
	} {
		lock, err := catalog.LoadLockContext(t.Context(), filepath.Join(repo, "inputs/catalogs", tc.dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range tc.sources {
			if pin, ok := lock.Pins[source]; !ok || pin.Commit != nvke {
				t.Fatalf("%s/%s lost selected NVKE source", tc.dir, source)
			}
		}
	}
	for _, conformance := range []bool{false, true} {
		path, _, err := LocalArtifactInputsFor("ovn", conformance)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(repo, path)
		doc, err := authored.LoadDefinition(t.Context(), file)
		if err != nil {
			t.Fatal(err)
		}
		want := 32
		if conformance {
			want = 36
		}
		if len(doc.Outputs) != want {
			t.Fatalf("outputs%d want%d", len(doc.Outputs), want)
		}
		expected := map[string]string{"nvke-node-image-arm64": "node-images/native-arm64", "cwe": "nvke/native-site-arm64", "nvkectl": "nvke/native-site-arm64", "controller-chart": "nvke/native-site-arm64", "controller-profiles": "controller-profiles/native-nvidia-arm64"}
		for _, output := range doc.Outputs {
			dir, selected := expected[output.Name]
			if !selected {
				continue
			}
			if output.Catalog == nil || filepath.Clean(filepath.Join(filepath.Dir(file), output.Catalog.Base)) != filepath.Join(repo, "inputs/catalogs", dir) {
				t.Fatalf("%s lost its native catalog selection", output.Name)
			}
			delete(expected, output.Name)
		}
		if len(expected) != 0 {
			t.Fatalf("missing native outputs: %v", expected)
		}
	}
}
