// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeTenantRequestSelectsReviewedAPI(t *testing.T) {
	for _, tc := range []struct {
		name, commit, subnet string
		modern, rejected     bool
	}{
		{"modern Mac", "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "192.168.64.0/24", true, false},
		{"modern Linux", "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "172.30.0.0/24", true, false},
		{"historical", "b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n", "172.30.0.0/24", false, false},
		{"unreviewed", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", "192.168.64.0/24", false, true},
		{"pod overlap", "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "10.245.0.0/24", true, true},
		{"service overlap", "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "10.97.0.0/24", true, true},
		{"missing underlay", "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installer := privateTestDir(t)
			writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte(tc.commit), 0600)
			subnet, _ := netip.ParsePrefix(tc.subnet)
			attempt := nativeSiteAttempt{assets: NativeAssets{Installer: installer}, network: "upstream-ovn", bridge: nativeBridgeConfig{prefix: subnet}}
			request, err := attempt.tenantRequest(t.Context())
			if tc.rejected {
				if err == nil || !reflect.DeepEqual(request, TenantRequest{}) {
					t.Fatalf("rejected selection produced intent: %+v %v", request, err)
				}
				if tc.modern {
					var coded *nvkrerr.Error
					if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
						t.Fatalf("overlap admission: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if _, present := wire["controlPlaneVIP"]; present {
				t.Fatal("no-VIP intent must omit the optional field")
			}
			var decoded struct {
				ControlPlane struct {
					NICo map[string]json.RawMessage `json:"nico"`
				} `json:"controlPlaneConfig"`
				NodePools []struct {
					NICo map[string]json.RawMessage `json:"nico"`
				} `json:"nodePools"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.NodePools) != 1 {
				t.Fatal("worker topology changed")
			}
			selected, absent := "osProfile", "release"
			if tc.modern {
				selected, absent = "release", "osProfile"
			}
			for _, provider := range []map[string]json.RawMessage{decoded.ControlPlane.NICo, decoded.NodePools[0].NICo} {
				var release string
				if err := json.Unmarshal(provider[selected], &release); err != nil || release != TenantProfile {
					t.Fatalf("wrong selected release: %s %v", provider[selected], err)
				}
				if _, present := provider[absent]; present {
					t.Fatalf("source received incompatible field %s", absent)
				}
			}
			if tc.modern {
				if request.Network == nil {
					t.Fatal("missing explicit tenant networks")
				}
				pod, service := netip.MustParsePrefix(request.Network.PodCIDR), netip.MustParsePrefix(request.Network.ServiceCIDR)
				if pod.Overlaps(service) || pod.Overlaps(subnet) || service.Overlaps(subnet) {
					t.Fatal("tenant network overlap")
				}
				for _, network := range []string{managementPodCIDR, managementServiceCIDR} {
					if pod.Overlaps(netip.MustParsePrefix(network)) || service.Overlaps(netip.MustParsePrefix(network)) {
						t.Fatal("tenant networks overlap site")
					}
				}
			} else if request.Network != nil {
				t.Fatal("historical request changed unrelated networking")
			}
			request.VIP = TenantVIP
			request.Network = nil
			request.ControlPlane.NICo.OS = TenantProfile
			request.ControlPlane.NICo.Release = ""
			request.NodePools[0].NICo.OS = TenantProfile
			request.NodePools[0].NICo.Release = ""
			want, err := newTenantRequest("native-demo-tenant", "upstream-ovn")
			if err != nil || !reflect.DeepEqual(request, want) {
				t.Fatalf("unrelated topology changed: %+v %v", request, err)
			}
		})
	}
}

func TestNativeTenantRequestRejectsCanceledSourceRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	attempt := nativeSiteAttempt{network: "upstream-ovn", assets: NativeAssets{Installer: privateTestDir(t)}}
	request, err := attempt.tenantRequest(ctx)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(request, TenantRequest{}) {
		t.Fatalf("canceled intent: %+v %v", request, err)
	}
}
