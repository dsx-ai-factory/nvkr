// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Compile the maintained selections through the owning boundary: a modern CLI
// paired with legacy disk packages or a different controller cannot bootstrap.
func TestNativeNVIDIADefinitionsAlignRenamedSourceContracts(t *testing.T) {
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
