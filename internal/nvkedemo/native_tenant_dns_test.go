// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
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
	for _, tc := range []struct {
		name, fail, block string
	}{
		{name: "ready"},
		{name: "CoreDNS failure", fail: "deployment/coredns"},
		{name: "node-local failure", fail: "daemonset/node-local-dns"},
		{name: "rollout deadline", block: "deployment/coredns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, root := tenantRunFixture(t, "ovn", tc.fail, tc.block)
			rows, err := json.Marshal([]tenantRow{{Name: "native-demo-tenant", ID: "owned-id", NICo: TenantProvider{OrgID, SiteID, VPCID}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "rows.json"), rows, 0600); err != nil {
				t.Fatal(err)
			}
			installer := privateTestDir(t)
			writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte("69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n"), 0600)
			attempt := nativeSiteAttempt{
				assets: NativeAssets{Installer: installer}, network: "ovn", tenantTimeout: 5 * time.Second,
				bridge: nativeBridgeConfig{prefix: netip.MustParsePrefix(GuestCIDR)},
			}
			ctx := t.Context()
			if tc.block != "" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			err = attempt.runTenant(ctx, selected.session.Runner, filepath.Join(selected.session.Work, "cli", installerCLIName), selected.session.Tools, selected.session.Work, "site.config")
			want := []string{"kubeconfig", "nodes", "deployment/coredns"}
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
