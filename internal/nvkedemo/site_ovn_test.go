// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUpstreamOVNProfilesRejectOppositeModeRun(t *testing.T) {
	for _, network := range []string{"ovn", "kind-calico", "upstream-ovn", "unknown"} {
		t.Run(network, func(t *testing.T) {
			selected, err := selectedProfilesOutput(network)
			if network == "unknown" {
				assertConfigUsage(t, err)
				return
			}
			if err != nil || (selected == "controller-profiles-upstream-ovn") != (network == "upstream-ovn") {
				t.Fatal("profiles selection differs from the network", selected, err)
			}
			root := privateTestDir(t)
			record := fixtureRun()
			expected, observed := record["expected"].([]map[string]any), record["observed"].([]map[string]any)
			expected[1]["name"], expected[1]["kind"] = selected, "oci"
			observed[1]["name"], observed[1]["kind"] = selected, "oci"
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "lifecycle.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = CompleteOutput(t.Context(), root, selected, "oci"); err != nil {
				t.Fatal("selected profiles output rejected", err)
			}
			foreign := "controller-profiles-upstream-ovn"
			if network == "upstream-ovn" {
				foreign = "controller-profiles"
			}
			if _, err = CompleteOutput(t.Context(), root, foreign, "oci"); err == nil {
				t.Fatal("opposite-mode profiles replay accepted")
			}
		})
	}
}

func TestUpstreamOVNRequiresDistinctCompletedOutputSlots(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn", "kind-calico", "unknown"} {
		t.Run(network, func(t *testing.T) {
			version, imageName, chartName, err := selectedOVNOutputs(network)
			if network == "kind-calico" || network == "unknown" {
				assertConfigUsage(t, err)
				return
			}
			disk, err := selectedDiskOutput(network)
			if err != nil {
				t.Fatal(err)
			}
			if network == "upstream-ovn" && (version != "1.4.0" || imageName != "upstream-ovn-image-arm64" || chartName != "upstream-ovn-chart-arm64" || disk != "nvke-node-image-upstream-ovn-arm64") {
				t.Fatal("upstream selection reused baseline outputs")
			}
			if network == "ovn" && (version != siteOVNVersion || imageName != "ovn-image-arm64" || chartName != "ovn-chart-arm64" || disk != DiskOutputName) {
				t.Fatal("maintained output selection changed")
			}
		})
	}
}

func TestSiteCIDRsRejectOverlap(t *testing.T) {
	for _, cidr := range []string{"172.18.0.0/16", "10.244.0.0/16", "10.240.0.0/12", "10.96.0.0/16", "10.0.0.0/8", "unknown"} {
		t.Run(cidr, func(t *testing.T) {
			if err := admitSiteCIDRs(cidr); (err == nil) != (cidr == "172.18.0.0/16") {
				t.Fatalf("network overlap admission: %v", err)
			}
		})
	}
}
