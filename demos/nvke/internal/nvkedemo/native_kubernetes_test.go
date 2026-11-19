// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeKubernetesUsesSelectedNVKESiteDefault(t *testing.T) {
	for _, subnet := range []string{"192.168.64.0/24", GuestCIDR} {
		for _, tc := range []struct {
			name, raw, want            string
			changed, missing, canceled bool
		}{
			{name: "historical fixture", raw: "cluster:\n  k8sVersion: 1.34.2\n", want: "1.34.2"},
			{name: "selected 1.36", raw: "cluster:\n  k8sVersion: 1.36.3\n", want: "1.36.3"},
			{name: "another source version", raw: "cluster:\n  k8sVersion: 1.35.3\n", want: "1.35.3"},
			{name: "absent version", raw: "cluster: {}\n"},
			{name: "prefixed version", raw: "cluster:\n  k8sVersion: v1.36.3\n"},
			{name: "prerelease", raw: "cluster:\n  k8sVersion: 1.36.3-rc.1\n"},
			{name: "duplicate version", raw: "cluster:\n  k8sVersion: 1.36.3\n  k8sVersion: 1.34.2\n"},
			{name: "changed after extraction", raw: "cluster:\n  k8sVersion: 1.36.3\n", changed: true},
			{name: "missing source template", missing: true},
			{name: "canceled", canceled: true},
		} {
			t.Run(subnet+"/"+tc.name, func(t *testing.T) {
				attempt := nativeCAPITenantFixture(t, subnet, "ovn")
				path := filepath.Join(attempt.assets.Installer, nativeSiteSpecPath)
				if tc.missing {
					if err := os.Rename(path, path+".held"); err != nil {
						t.Fatal(err)
					}
				} else {
					writeTenantFixture(t, path, []byte(tc.raw), 0600)
					if !tc.changed {
						attempt.assets.installerSelection.siteSpecDigest = plainSHA([]byte(tc.raw))
					}
				}
				ctx, cancel := context.WithCancel(startupTestContext(t))
				defer cancel()
				if tc.canceled {
					cancel()
				}
				version, err := nativeKubernetesVersion(ctx, attempt.assets)
				if tc.want == "" {
					if err == nil || version != "" {
						t.Fatalf("unavailable or changed version accepted: %q %v", version, err)
					}
					if request, err := attempt.namedTenantRequest(ctx, "native-demo-tenant"); err == nil || request.Kubernetes != "" {
						t.Fatal("unadmitted version reached tenant intent", err)
					}
					return
				}
				if err != nil || version != tc.want {
					t.Fatalf("version %q, want %q: %v", version, tc.want, err)
				}
				request, err := attempt.namedTenantRequest(ctx, "native-demo-tenant")
				if err != nil || request.Kubernetes != tc.want {
					t.Fatalf("tenant lost selected Kubernetes version: %q %v", request.Kubernetes, err)
				}
				cfg := startupDescription()
				cfg.KubernetesVersion = version
				startup, err := SiteStartupUserData(ctx, cfg)
				if err != nil || !strings.Contains(startup, "nvke-kubeadm-distro-"+tc.want) {
					t.Fatal("site package differs from selected source default", err)
				}
				nodes := readyTenantFixture()
				for i := range nodes {
					nodes[i].Status.NodeInfo.KubeletVersion = "v" + version
				}
				if ready, err := readyNodesForCountVersion(nodes, version, 2); err != nil || !ready {
					t.Fatal("matching native nodes rejected", err)
				}
				nodes[1].Status.NodeInfo.KubeletVersion = "v1.33.0"
				if _, err := readyNodesForCountVersion(nodes, version, 2); err == nil {
					t.Fatal("foreign Kubernetes worker accepted")
				}
			})
		}
	}
}
