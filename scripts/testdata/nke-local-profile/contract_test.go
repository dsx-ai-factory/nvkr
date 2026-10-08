// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copy this diagnostic test into a private copy of the pinned NKE utils
// package, never a source checkout or accepted Run. NKE owns interpretation.
// This checks content, not boot, deployment or attestation compatibility.
package utils

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

func TestNKDLocalProfile(t *testing.T) {
	root := os.Getenv("NKD_PROFILES_DIR")
	if root == "" {
		t.Fatal("NKD_PROFILES_DIR must select extracted built content")
	}
	idx, err := LoadOSProfileMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	name := "nke-local-26-04-arm64-plain"
	meta, ok := idx.GetOSProfileMetadata(name, false)
	if !ok || len(idx.Profiles) != 1 || meta.Architecture != "arm64" || meta.UbuntuVersion != "26.04" || meta.Variant != "local-plain-tpm" || !meta.KubernetesVersionInjected {
		t.Fatalf("incorrect profile metadata: %+v", idx)
	}
	if len(idx.FindOSProfiles("1.34.2", "arm64", "local-plain-tpm", "26.04")) != 1 || len(idx.FindOSProfiles("1.34.2", "amd64", "local-plain-tpm", "26.04")) != 0 || len(idx.FindOSProfiles("1.34.2", "arm64", "local-plain-tpm", "24.04")) != 0 {
		t.Fatal("selection does not distinguish OS/architecture")
	}
	data, err := os.ReadFile(filepath.Join(root, "osProfiles", name, "userData.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "carbide-pxe") {
		t.Fatal("the extra HTTP agent-download override remains")
	}
	tmpl, err := template.New("userData").Option("missingkey=error").Funcs(template.FuncMap{"quote": Quote, "indent": Indent, "trimPrefix": TrimPrefix}).Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		calico, ovn   bool
		wantedPackage string
	}{
		{"Calico", true, false, "nke-cni-calico-images-3.30.1"},
		{"OVN", false, true, "nke-cni-ovn-charts-1.4.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := tmpl.Execute(&out, map[string]any{
				"Hostname": "nkd-local-node", "TeleportEnabled": false,
				"SpireServerAddress": "172.30.0.10", "SpireServerPort": "8081",
				"TrustDomain": "nkd-local.test", "TrustBundle": "diagnostic trust bundle",
				"Files": []any{}, "KubernetesVersion": "v1.34.2",
				"CalicoEnabled": tc.calico, "OvnEnabled": tc.ovn,
			})
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal(out.Bytes(), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg["hostname"] != "nkd-local-node" || !strings.Contains(out.String(), "nke-kubeadm-distro-1.34.2") || !strings.Contains(out.String(), tc.wantedPackage) || !strings.Contains(out.String(), "tpm_attestor_agent") {
				t.Fatal("rendered cloud-init lacks expected packages/TPM route")
			}
		})
	}
}
