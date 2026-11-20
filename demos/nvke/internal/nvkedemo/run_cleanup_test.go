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
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRestoreWithdrawsControlsDespiteENOSPC(t *testing.T) {
	t.Run("restore", func(t *testing.T) {
		g := networkFixture(t)
		path := filepath.Join(g.Work, "nico-sim")
		if err := os.WriteFile(path, []byte("private simulator bytes"), 0o700); err != nil {
			t.Fatal(err)
		}
		id, digest, err := networkFile(t.Context(), path, true)
		if err != nil {
			t.Fatal(err)
		}
		g.caps = []networkCapability{{path, id, digest, networkCapabilities["nico-sim"]}}
		g.rules = networkRules(g.Owner, g.KindCIDR)[:1]
		g.sysctlIntents = []string{networkSysctls[0]}
		g.BGPIntent, g.BridgeIntent, g.BridgeID = true, true, "42"
		if err = g.Checkpoint(t.Context()); err != nil {
			t.Fatal(err)
		}
		g.checkpointHook = func() error { return unix.ENOSPC }
		capPresent, rulePresent, bridgePresent, routePresent := true, true, true, true
		sysctl, verified := "1", false
		var mutations []string
		g.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
			switch args[0] {
			case "ps", "virsh":
				return nil, nil
			case "ip":
				if slices.Equal(args, []string{"ip", "-j", "route", "show", "table", "main"}) {
					if routePresent {
						return []byte(`[{"dst":"172.31.0.0/24","type":"blackhole","flags":[]}]`), nil
					}
					return []byte("[]"), nil
				}
				if slices.Equal(args, []string{"ip", "route", "delete", "blackhole", VIPCIDR, "table", "main"}) {
					if !privileged {
						t.Fatal("route withdrawal without admitted privilege")
					}
					mutations = append(mutations, "route")
					routePresent = false
					return nil, nil
				}
				if slices.Equal(args, []string{"ip", "link", "delete", BridgeName, "type", "bridge"}) {
					mutations = append(mutations, "bridge")
					bridgePresent = false
					return nil, nil
				}
				if !bridgePresent {
					return []byte("[]"), nil
				}
				return []byte(fmt.Sprintf(`[{"ifname":"nvkevm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, g.BridgeMAC)), nil
			case "getcap":
				if capPresent {
					return []byte("opaque-libcap-presence"), nil
				}
				return nil, nil
			case "setcap":
				if args[1] == "-v" {
					verified = true
				}
				if args[1] == "-r" {
					if !verified {
						t.Fatal("unverified capability removed")
					}
					mutations = append(mutations, "cap")
					capPresent = false
				}
				return nil, nil
			case "iptables":
				if args[5] == "-C" && !rulePresent {
					return nil, &CommandError{ExitCode: 1, Cause: errors.New("absent")}
				}
				if args[5] == "-D" {
					mutations = append(mutations, "rule")
					rulePresent = false
				}
				return nil, nil
			case "sysctl":
				if args[1] == "-n" {
					return []byte(sysctl), nil
				}
				mutations = append(mutations, "sysctl")
				sysctl = "0"
				return nil, nil
			default:
				t.Fatalf("unexpected cleanup command %v", args)
				return nil, nil
			}
		}
		err = restoreNetwork(t.Context(), g)
		if !errors.Is(err, unix.ENOSPC) {
			t.Fatalf("cleanup must not report PASS on persistence failure: %v", err)
		}
		if capPresent || rulePresent || bridgePresent || routePresent || sysctl != "0" {
			t.Fatalf("controls retained after ENOSPC: %v", mutations)
		}
		if !slices.Equal(mutations, []string{"route", "cap", "rule", "sysctl", "bridge"}) {
			t.Fatalf("withdrawal order=%v", mutations)
		}
		state, readErr := readNetworkState(g.Work)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if state.Finished || !state.BGPIntent || !state.BridgeIntent {
			t.Fatal("failed persistence fabricated completed durable cleanup")
		}
	})
}

func bgpRows(t *testing.T, value string) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestBGPRoutesRequireExactOwnedShape(t *testing.T) {
	for _, row := range []struct {
		name, value   string
		bridge, valid bool
		selected      int
	}{
		{"blackhole", `[{"dst":"172.31.0.0/24","type":"blackhole","flags":[]}]`, false, true, 1},
		{"vip", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"nvkevm0","flags":[]}]`, true, true, 1},
		{"linkdown", `[{"dst":"172.31.0.10/32","gateway":"172.30.0.10","dev":"nvkevm0","flags":["linkdown"]}]`, true, true, 1},
		{"multipath", `[{"dst":"172.31.0.10","nexthops":[{"gateway":"172.30.0.10","dev":"nvkevm0","weight":1},{"gateway":"172.30.0.11","dev":"nvkevm0","flags":[]}]}]`, true, true, 1},
		{"unrelated", `[{"dst":"default","gateway":"192.168.1.1"},{"dst":"10.2.0.0/16","dev":"other"}]`, false, true, 0},
		{"no-bridge", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"nvkevm0"}]`, false, false, 0},
		{"foreign-gateway", `[{"dst":"172.31.0.10","gateway":"192.168.1.10","dev":"nvkevm0"}]`, true, false, 0},
		{"foreign-device", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"other"}]`, true, false, 0},
		{"unknown-flags", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"nvkevm0","flags":["onlink"]}]`, true, false, 0},
		{"null-flags", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"nvkevm0","flags":null}]`, true, false, 0},
		{"extra-field", `[{"dst":"172.31.0.10","gateway":"172.30.0.10","dev":"nvkevm0","metric":3}]`, true, false, 0},
		{"weighted-hop", `[{"dst":"172.31.0.10","nexthops":[{"gateway":"172.30.0.10","dev":"nvkevm0","weight":2}]}]`, true, false, 0},
		{"empty-hops", `[{"dst":"172.31.0.10","nexthops":[]}]`, true, false, 0},
		{"malformed-hop", `[{"dst":"172.31.0.10","nexthops":true}]`, true, false, 0},
		{"blackhole-extra", `[{"dst":"172.31.0.0/24","type":"blackhole","metric":3}]`, true, false, 0},
		{"blackhole-flags", `[{"dst":"172.31.0.0/24","type":"blackhole","flags":["linkdown"]}]`, true, false, 0},
		{"blackhole-null-flags", `[{"dst":"172.31.0.0/24","type":"blackhole","flags":null}]`, true, false, 0},
		{"other-vip", `[{"dst":"172.31.0.11","gateway":"172.30.0.10","dev":"nvkevm0"}]`, true, false, 0},
		{"unknown-overlap", `[{"dst":"172.31.0.0/16","dev":"nvkevm0"}]`, true, false, 0},
		{"malformed-prefix", `[{"dst":"not-a-route"}]`, true, false, 0},
		{"non-string-prefix", `[{"dst":true}]`, true, false, 0},
		{"empty-prefix", `[{"dst":""}]`, true, false, 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			selected, err := selectBGPRoutes(bgpRows(t, row.value), row.bridge)
			if (err == nil) != row.valid || row.valid && len(selected) != row.selected {
				t.Fatalf("selected=%v err=%v", selected, err)
			}
		})
	}
}

func TestBGPWithdrawalRechecksExactRouteAndFinalAbsence(t *testing.T) {
	for _, name := range []string{"success", "changed", "disappeared", "remaining", "inventory-failed", "active-simulator", "foreign-bridge", "unjoined-delete", "previous-unjoined"} {
		t.Run(name, func(t *testing.T) {
			g := networkFixture(t)
			g.BridgeIntent, g.BridgeID = true, "42"
			if name == "previous-unjoined" {
				g.unjoined.Store(true)
			}
			inventories, deletions := 0, 0
			g.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
				if args[0] == "ps" {
					if name == "active-simulator" {
						return []byte("nico-sim\n"), nil
					}
					return nil, nil
				}
				if args[0] == "virsh" {
					return nil, nil
				}
				if slices.Equal(args, []string{"ip", "-j", "route", "show", "table", "main"}) {
					inventories++
					if name == "inventory-failed" && inventories == 2 {
						return nil, errors.New("route inventory unavailable")
					}
					if name == "changed" && inventories == 2 {
						return []byte(`[{"dst":"172.31.0.0/24","type":"blackhole","metric":3}]`), nil
					}
					if name == "disappeared" && inventories == 2 || inventories >= 3 && name != "remaining" {
						return []byte("[]"), nil
					}
					return []byte(`[{"dst":"172.31.0.0/24","type":"blackhole","flags":[]}]`), nil
				}
				if slices.Equal(args, []string{"ip", "route", "delete", "blackhole", VIPCIDR, "table", "main"}) {
					if !privileged {
						t.Fatal("unprivileged route deletion")
					}
					deletions++
					if name == "unjoined-delete" {
						return nil, ErrUnjoined
					}
					return nil, nil
				}
				mac := g.BridgeMAC
				if name == "foreign-bridge" {
					mac = "02:00:00:00:00:00"
				}
				return []byte(fmt.Sprintf(`[{"ifname":"nvkevm0","ifindex":42,"address":%q,"linkinfo":{"info_kind":"bridge"}}]`, mac)), nil
			}
			err := restoreBGP(t.Context(), g)
			if (err == nil) != (name == "success") {
				t.Fatalf("restore result=%v", err)
			}
			wantDeletes := 0
			if slices.Contains([]string{"success", "remaining", "unjoined-delete"}, name) {
				wantDeletes = 1
			}
			if deletions != wantDeletes {
				t.Fatalf("deletions=%d want=%d", deletions, wantDeletes)
			}
			if name == "unjoined-delete" && (!errors.Is(err, ErrUnjoined) || !g.unjoined.Load()) {
				t.Fatal("ambiguous delete lost unjoined state")
			}
		})
	}
}

func TestRestoreRefusesUnknownDomainWithoutHostMutation(t *testing.T) {
	for _, value := range []string{"foreign-domain", "query-failed"} {
		t.Run(value, func(t *testing.T) {
			g := networkFixture(t)
			g.BGPIntent = true
			mutations := 0
			g.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
				if privileged {
					mutations++
				}
				switch args[0] {
				case "ps":
					return nil, nil
				case "virsh":
					if value == "query-failed" {
						return nil, errors.New("domain inventory unavailable")
					}
					return []byte("foreign-domain\n"), nil
				default:
					t.Fatalf("unexpected restoration command %v", args)
					return nil, nil
				}
			}
			err := restoreNetwork(t.Context(), g)
			if err == nil || mutations != 0 || !g.BGPIntent {
				t.Fatalf("unsafe restoration: %v mutations=%d bgp=%v", err, mutations, g.BGPIntent)
			}
			if strings.Contains(err.Error(), "PASS") {
				t.Fatal("refused restoration reported PASS")
			}
		})
	}
}
