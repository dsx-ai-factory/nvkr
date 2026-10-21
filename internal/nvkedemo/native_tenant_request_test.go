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
		{"modern Mac", "dfc817aa34205e8099dad91dfd05170499d00b60\n", "192.168.64.0/24", true, false},
		{"modern Linux", "dfc817aa34205e8099dad91dfd05170499d00b60\n", "172.30.0.0/24", true, false},
		{"historical", "b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n", "172.30.0.0/24", false, true},
		{"unreviewed", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", "192.168.64.0/24", false, true},
		{"pod overlap", "dfc817aa34205e8099dad91dfd05170499d00b60\n", "10.245.0.0/24", true, true},
		{"service overlap", "dfc817aa34205e8099dad91dfd05170499d00b60\n", "10.97.0.0/24", true, true},
		{"missing underlay", "dfc817aa34205e8099dad91dfd05170499d00b60\n", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := nativeCAPITenantFixture(t, tc.subnet, "upstream-ovn")
			writeTenantFixture(t, filepath.Join(attempt.assets.Installer, "SOURCE_COMMIT"), []byte(tc.commit), 0600)
			subnet := attempt.bridge.prefix
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
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
			if _, present := wire["nico"]; present || request.Infra == nil || request.ControlPlane.Infra == nil || len(request.NodePools) != 1 || request.NodePools[0].Infra == nil {
				t.Fatal("native topology must use only generic CAPI provider fields")
			}
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
			if request.ControlPlane.Count != 1 || request.NodePools[0].Count != 1 || request.NodePools[0].ControlPlane {
				t.Fatal("native topology must keep one control plane and one worker")
			}
		})
	}
}

func TestNativeTenantRequestRejectsCanceledSourceRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	attempt := nativeSiteAttempt{network: "upstream-ovn", assets: NativeAssets{Installer: privateTestDir(t)}}
	request, err := attempt.namedTenantRequest(ctx, "native-demo-tenant")
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(request, TenantRequest{}) {
		t.Fatalf("canceled intent: %+v %v", request, err)
	}
}
