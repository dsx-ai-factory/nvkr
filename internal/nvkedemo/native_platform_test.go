// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeHostRejectsUnsupportedBackendBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		platform, accelerator string
		accepted              bool
	}{
		{"darwin/arm64", "hvf", true}, {"darwin/arm64", "tcg", true}, {"linux/arm64", "kvm", true},
		{"linux/arm64", "hvf", false}, {"linux/arm64", "tcg", false}, {"darwin/arm64", "kvm", false},
		{"linux/amd64", "kvm", false}, {"windows/arm64", "kvm", false}, {"linux/arm64", "", false},
	} {
		t.Run(tc.platform+"/"+tc.accelerator, func(t *testing.T) {
			err := admitNativeHost(tc.platform, tc.accelerator)
			if tc.accepted {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
				t.Fatalf("unsupported backend accepted: %v", err)
			}
		})
	}
}

func TestNativeTCGRequiresAdapterExecutableBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		platform, accelerator, executable string
		site, accepted                    bool
	}{
		{"darwin/arm64", "tcg", "", false, false},
		{"darwin/arm64", "tcg", "relative-demo", false, false},
		{"darwin/arm64", "tcg", "/local/bin/nvke-demo", false, true},
		{"darwin/arm64", "hvf", "", false, true},
		{"darwin/arm64", "hvf", "", true, false},
		{"darwin/arm64", "hvf", "/local/bin/nvke-demo", true, true},
		{"linux/arm64", "kvm", "", true, true},
	} {
		t.Run(tc.platform+tc.accelerator+tc.executable+map[bool]string{true: "site", false: "guest"}[tc.site], func(t *testing.T) {
			err := admitNativeQEMUAdapter(tc.platform, tc.accelerator, tc.executable, tc.site)
			if (err == nil) != tc.accepted {
				t.Fatal(err)
			}
			if !tc.accepted {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
					t.Fatal("uncoded adapter refusal", err)
				}
			}
		})
	}
}

func TestNativeNetworkJournalAdmitsOnlyExactBoundedGrants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func([]networkRule) []networkRule
		accepted bool
	}{
		{"all owned grants", func(r []networkRule) []networkRule { return r }, true},
		{"changed destination", func(r []networkRule) []networkRule { r[0].Args[9] = "8.8.8.8/32"; return r }, false},
		{"changed port", func(r []networkRule) []networkRule { r[0].Args[13] = "22"; return r }, false},
		{"duplicate grant", func(r []networkRule) []networkRule { return append(r, r[0]) }, false},
		{"wide source", func(r []networkRule) []networkRule { r[3].Args[5] = "0.0.0.0/0"; return r }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := networkFixture(t)
			state := guard.snapshot()
			state.Rules = append(networkRules(state.Owner, state.KindCIDR), tc.change(nativeBootstrapRules(state.Owner, state.KindCIDR))...)
			err := validateNetworkState(state, guard.Work)
			if (err == nil) != tc.accepted {
				t.Fatalf("native durable grant admission: %v", err)
			}
		})
	}
}

func TestNativeHostTransportJournalHasNoKindBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*networkState)
		valid  bool
	}{
		{"native", func(*networkState) {}, true},
		{"legacy cannot omit subnet", func(s *networkState) { s.Version = 3 }, false},
		{"native cannot invent Kind subnet", func(s *networkState) { s.KindCIDR = "172.18.0.0/16" }, false},
		{"native cannot carry Kind gateway", func(s *networkState) { s.KindGateway = "172.18.0.1" }, false},
		{"native cannot carry BGP intent", func(s *networkState) { s.BGPIntent = true }, false},
		{"native cannot grant Kind forwarding", func(s *networkState) {
			s.Rules = append(s.Rules, nativeBootstrapRules(s.Owner, "172.18.0.0/16")[3])
		}, false},
		{"native cannot grant Kind raw traffic", func(s *networkState) {
			s.Rules = append(s.Rules, guestTCPRule(s.Owner, "172.18.0.2", 30440))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := networkFixture(t)
			guard.KindCIDR = ""
			state := guard.snapshot()
			if state.Version != 4 {
				t.Fatal("native journal did not select its versioned transport contract")
			}
			state.Rules = append(networkRules(state.Owner, ""), nativeBootstrapRules(state.Owner, "")...)
			tc.change(&state)
			if err := validateNetworkState(state, guard.Work); (err == nil) != tc.valid {
				t.Fatalf("native transport journal admission: %v", err)
			}
			if tc.valid {
				guard.rules = state.Rules
				if err := guard.Checkpoint(t.Context()); err != nil {
					t.Fatal(err)
				}
				if restored, err := readNetworkState(guard.Work); err != nil || restored.Version != 4 || restored.KindCIDR != "" {
					t.Fatalf("native recovery contract changed: %+v %v", restored, err)
				}
			}
		})
	}
}

func TestNativeNetworkScopeDoesNotWeakenLegacySubnetAdmission(t *testing.T) {
	for _, native := range []bool{false, true} {
		guard, err := newNetworkGuardScope(Runner{Work: privateTestDir(t)}, "", native)
		if native {
			if err != nil || guard.KindCIDR != "" {
				t.Fatalf("native host transport: %v", err)
			}
			guard.releaseHandles()
		} else if err == nil || guard != nil {
			t.Fatal("legacy guard accepted an absent Kind subnet")
		}
	}
}
