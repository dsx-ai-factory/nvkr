// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCloseDeletesTenantNaturallyBeforeBootstrap(t *testing.T) {
	root := privateTestDir(t)
	events, deleted := filepath.Join(root, "events"), filepath.Join(root, "deleted")
	row := tenantRow{Name: "native-demo-tenant", ID: "owned-native-id", NICo: TenantProvider{OrgID, SiteID, VPCID}}
	encoded, err := json.Marshal([]tenantRow{row})
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "nvkectl")
	script := `#!/bin/sh
set -eu
case "$1:$2" in
 get:clusters) printf 'inventory\n' >> "$EVENTS"; if test -f "$DELETED"; then printf '[]'; else printf '%s' "$ROW"; fi ;;
 delete:cluster) test "$3" = native-demo-tenant; printf 'natural-delete\n' >> "$EVENTS"; touch "$DELETED" ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kind"), []byte("#!/bin/sh\nset -eu\ntest -f \"$DELETED\"\nprintf 'bootstrap-delete\\n' >> \"$EVENTS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "EVENTS=" + events, "DELETED=" + deleted, "ROW=" + string(encoded)}}
	attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root}, bootstrapName: "owned-bootstrap", bootstrapRunner: &runner,
		tenantOwner: &tenant{name: row.Name, id: row.ID, intent: true, cliPath: cli, session: ManagementSession{Work: root, Runner: runner}}}
	if err := attempt.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "inventory\ninventory\nnatural-delete\ninventory\nbootstrap-delete\n" {
		t.Fatalf("cleanup owner order: %s", data)
	}
	retained, err := os.ReadFile(filepath.Join(root, "tenant-cleanup-observations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(retained), `"exitCode":0`) {
		t.Fatal("natural cleanup observations were not retained")
	}
}

func TestNativeCleanupRejectsForeignRowsWithoutLosingOwnedDeletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     []nativeInstance
		id       string
		site     bool
		want     int
		rejected bool
	}{
		{"guest", []nativeInstance{{ID: "owned", Name: "nvke-native-guest"}}, "owned", false, 1, false},
		{"foreign first", []nativeInstance{{ID: "foreign", Name: "other"}, {ID: "owned", Name: "nvke-native-guest"}}, "owned", false, 1, true},
		{"foreign only", []nativeInstance{{ID: "foreign", Name: "other"}}, "owned", false, 0, true},
		{"wrong identity", []nativeInstance{{ID: "other", Name: "nvke-native-guest"}}, "owned", false, 0, true},
		{"lost create reply", []nativeInstance{{ID: "owned", Name: "nvke-native-guest"}}, "", false, 1, false},
		{"site", []nativeInstance{{ID: "site"}, {ID: "tenant"}}, "site", true, 2, false},
		{"empty identity", []nativeInstance{{}}, "", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := 1
			if tc.site {
				limit += nativeTenantNodeCount(false)
			}
			owned, err := nativeCleanupRows(tc.rows, tc.id, limit)
			if len(owned) != tc.want || (err != nil) != tc.rejected {
				t.Fatalf("owned=%v err=%v", owned, err)
			}
		})
	}
}
