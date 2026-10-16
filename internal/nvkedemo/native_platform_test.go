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
