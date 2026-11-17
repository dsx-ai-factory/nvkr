// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestNativeSmallProfilePreservesBaselineDefaultBytes(t *testing.T) {
	// Exact-byte snapshots independently captured from PR base
	// 334db4d5cfcc53c8dc4316830de1f4fe093944f2 in a separate clean source tree.
	// The startup fixture now uses the renamed nvke-controller-profiles image.
	startup, err := SiteStartupUserData(startupTestContext(t), startupDescription())
	if err != nil {
		t.Fatal(err)
	}
	telemetry, err := demoTelemetryValues()
	if err != nil {
		t.Fatal(err)
	}
	ref := "localhost/nvke/ovn-kubernetes@sha256:" + strings.Repeat("a", 64)
	nvidia, err := nativeOVNValues(siteOVNVersion, ref)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := nativeOVNValues(upstreamSiteOVNVersion, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"startup", []byte(startup), "1a573af4c29df4ae37126e8bcd5491689f0dd874161da7e3a2461aa424a14f53"},
		{"telemetry", telemetry, "724ea863abea959ff0c1ef30a07457f2e94f1f4d13ba8350a908fa53671910b5"},
		{"NVIDIA OVN", []byte(nvidia), "7502273181c4c11400e4724075443b2bd640f4aac74729a30f8f5dfdc295dac4"},
		{"upstream OVN", []byte(upstream), "426c3268fec023c938cbf3e638ec182ddc8291455dcd8ce3a7139b0663c15691"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmt.Sprintf("%x", sha256.Sum256(tc.raw)); got != tc.want {
				t.Fatalf("unset profile changed default bytes: %s", got)
			}
		})
	}
}
