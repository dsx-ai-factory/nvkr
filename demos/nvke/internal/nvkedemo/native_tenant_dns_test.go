// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the native orchestration with owned fixture commands, not guests.
// Ready nodes must not bypass DNS admission or permit a smoke retry.
func TestNativeTenantAdmitsDNSBeforeSingleSmoke(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { nativeTenantDNSCases(t, count) })
	}
}

func nativeTenantDNSCases(t *testing.T, count int) {
	for _, tc := range []struct {
		name, fail, block string
	}{
		{name: "ready"},
		{name: "CoreDNS failure", fail: "deployment/coredns"},
		{name: "node-local failure", fail: "daemonset/node-local-dns"},
		{name: "rollout deadline", block: "deployment/coredns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, root := tenantRunFixture(t, tc.fail, tc.block)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Fixtures finish synchronously; the race runtime's default exit
			// sleep must not consume the selected DNS/readiness test budget.
			selected.session.Runner.Env = append(selected.session.Runner.Env, "CLOUDINIT_TOOL="+executable, "NVKR_TEST_CLOUD_INIT_TOOL=1", "GORACE=atexit_sleep_ms=0")
			attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
			workloadPods, err := json.Marshal(map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]string{"imageID": attempt.assets.Images["workload"].Reference}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(root, "pods.json"), workloadPods, 0600)
			attempt.tenantTimeout = 15 * time.Second
			if count == 3 {
				attempt.conformance = &nativeConformanceConfig{}
			}
			nodeBytes, err := json.Marshal(map[string]any{"items": nativeTopologyNodes(count)})
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(root, "nodes.json"), nodeBytes, 0600)
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
			if err != nil {
				t.Fatal(err)
			}
			rows := []tenantRow{{Name: request.Name, ID: "owned-id", Infra: request.Infra, ControlPlane: &request.ControlPlane}}
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(root, "rows.json"), encoded, 0600)
			if err := privateJSON(filepath.Join(root, "pools.json"), []map[string]any{{"id": "owned-worker", "name": "worker", "clusterID": "owned-id", "infra": request.NodePools[0].Infra, "desiredNodeCount": request.NodePools[0].Count}}); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if tc.block != "" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
			}
			err = attempt.runTenantRequest(ctx, selected.session.Runner, filepath.Join(selected.session.Work, "cli", installerCLIName), selected.session.Tools, selected.session.Work, "site.config", request)
			want := []string{"kubeconfig", "nodes", "nodes"}
			for i := range count {
				want = append(want, fmt.Sprintf("cloud-init-status-%d", i))
			}
			want = append(want, "nodes", "deployment/coredns")
			if tc.fail != "deployment/coredns" && tc.block == "" {
				want = append(want, "daemonset/node-local-dns")
			}
			if tc.fail != "" {
				if CommandExitCode(err) != 7 {
					t.Fatalf("rollout failure lost: %v", err)
				}
			} else if tc.block != "" {
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
					t.Fatalf("rollout deadline or child join lost: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, "workload-apply", "workload-complete", "workload-log", "workload-pod")
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("native readiness/workload order = %v; want %v", got, want)
			}
			if err != nil {
				if _, statErr := os.Stat(filepath.Join(selected.session.Work, "workload.json")); !os.IsNotExist(statErr) {
					t.Fatal("failed DNS gate admitted a smoke job")
				}
			}
		})
	}
}
