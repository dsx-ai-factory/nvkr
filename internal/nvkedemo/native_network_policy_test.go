// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

const nativeNetworkSourceTemplate = `#cloud-config
package_update: true
package_upgrade: false
write_files:
  - path: /etc/nke/trust-bundle.pem
    permissions: '0644'
    content: preserve-source-trust-bundle
runcmd:
  - systemctl stop nvke-agent.service
  - systemctl stop spire-agent.service
{{- if .OvnEnabled }}
  # The charts package is what the post-init CNI install reads.
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-1.4.0 nvke-cni-ovn-charts-1.4.0
{{- end }}
  - systemctl restart spire-agent.service
  - systemctl restart nvke-agent.service
`

func TestNativeOVNPackageProjectionMatchesTenantSelection(t *testing.T) {
	for _, network := range []string{"", "ovn", "upstream-ovn"} {
		for _, kind := range []string{"clusterrelease", "nodepoolrelease"} {
			t.Run(network+"/"+kind, func(t *testing.T) {
				spec := map[string]any{"machineInit": map[string]any{"userDataTemplate": nativeNetworkSourceTemplate, "preserve": "source"}, "unrelated": "source"}
				if err := nativeDemoOVNPackagePolicy(spec, network); err != nil {
					t.Fatal(err)
				}
				data := spec["machineInit"].(map[string]any)["userDataTemplate"].(string)
				request, err := newTenantRequest("native-demo-tenant", network)
				if err != nil {
					t.Fatal(err)
				}
				if network == "upstream-ovn" && data != nativeNetworkSourceTemplate {
					t.Fatal("explicit matched upstream source changed")
				}
				// The owning source renders the conditional later. Exercise its
				// standard template/cloud-config boundary and inspect the actual
				// package command against the consumer's selected chart stream.
				parsed, err := template.New(kind).Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				var rendered bytes.Buffer
				if err := parsed.Execute(&rendered, struct{ OvnEnabled bool }{true}); err != nil {
					t.Fatal(err)
				}
				var cloud struct {
					RunCmd []string `yaml:"runcmd"`
				}
				if err := yaml.Unmarshal(rendered.Bytes(), &cloud); err != nil {
					t.Fatal(err)
				}
				want := "DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-" + request.CNIVersion + " nvke-cni-ovn-charts-" + request.CNIVersion
				if len(cloud.RunCmd) != 5 || cloud.RunCmd[2] != want || cloud.RunCmd[0] != "systemctl stop nvke-agent.service" || cloud.RunCmd[1] != "systemctl stop spire-agent.service" || cloud.RunCmd[3] != "systemctl restart spire-agent.service" || cloud.RunCmd[4] != "systemctl restart nvke-agent.service" {
					t.Fatalf("rendered startup lost its matched package or source agent lifecycle: %q", cloud.RunCmd)
				}
				// Reversing only the selected package names must recover every
				// original byte, including trust configuration and unrelated data.
				recovered := strings.NewReplacer("nvke-cni-ovn-images-"+request.CNIVersion, "nvke-cni-ovn-images-1.4.0", "nvke-cni-ovn-charts-"+request.CNIVersion, "nvke-cni-ovn-charts-1.4.0").Replace(data)
				if recovered != nativeNetworkSourceTemplate || spec["unrelated"] != "source" || spec["machineInit"].(map[string]any)["preserve"] != "source" {
					t.Fatal("OVN selection changed unrelated source configuration")
				}
			})
		}
	}
}

func TestNativeOVNPackageProjectionRejectsChangedSourceWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name, data, network string
	}{
		{"missing command", strings.ReplaceAll(nativeNetworkSourceTemplate, "nvke-cni-ovn-", "other-cni-"), "ovn"},
		{"duplicate command", nativeNetworkSourceTemplate + nativeNetworkSourceTemplate, "ovn"},
		{"changed image version", strings.ReplaceAll(nativeNetworkSourceTemplate, "images-1.4.0", "images-2.4.0"), "ovn"},
		{"changed chart version", strings.ReplaceAll(nativeNetworkSourceTemplate, "charts-1.4.0", "charts-2.4.0"), "ovn"},
		{"chart version suffix", strings.ReplaceAll(nativeNetworkSourceTemplate, "charts-1.4.0", "charts-1.4.01"), "ovn"},
		{"changed source flags", strings.ReplaceAll(nativeNetworkSourceTemplate, "Timeout=600", "Timeout=601"), "ovn"},
		{"extra image command", nativeNetworkSourceTemplate + "  - apt-get install nvke-cni-ovn-images-2.4.0\n", "ovn"},
		{"extra chart command", nativeNetworkSourceTemplate + "  - apt-get install nvke-cni-ovn-charts-2.4.0\n", "ovn"},
		{"unknown format", strings.ReplaceAll(nativeNetworkSourceTemplate, "#cloud-config", "#!/bin/sh"), "ovn"},
		{"unknown network", nativeNetworkSourceTemplate, "other"},
		{"non-OVN network", nativeNetworkSourceTemplate, "kind-calico"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{"machineInit": map[string]any{"userDataTemplate": tc.data}, "unrelated": "preserve"}
			before, err := yaml.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if err := nativeDemoOVNPackagePolicy(spec, tc.network); err == nil {
				t.Fatal("unknown or ambiguous source accepted")
			}
			after, err := yaml.Marshal(spec)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("refused source was mutated")
			}
		})
	}
	for _, spec := range []map[string]any{nil, {}, {"machineInit": "invalid"}, {"machineInit": map[string]any{}}, {"machineInit": map[string]any{"userDataTemplate": 42}}} {
		before, err := yaml.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := nativeDemoOVNPackagePolicy(spec, "ovn"); err == nil {
			t.Fatal("missing or malformed source template accepted")
		}
		after, err := yaml.Marshal(spec)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("refused malformed source was mutated")
		}
	}
}
