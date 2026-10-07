// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package versionpolicy

import "testing"

func TestVersionPolicies(t *testing.T) {
	for _, tc := range []struct {
		value     string
		pin, helm bool
	}{
		{"1.2.3", true, true}, {"v1.2.3", true, true},
		{"1.2.3-beta.01", false, false}, {"1.2.3-01", false, false},
		{"1.2.3-beta.0", true, true}, {"1.2.3-0a", true, true},
		{"1.2.3+build.01", true, true}, {"v1.2.3-rc.1+build.01", true, true},
		{"1.2", false, true}, {"1", false, true}, {"01.2.3", false, true},
		{"1.2.x", false, false}, {">=1.2.3", false, false},
		{"", false, false}, {" 1.2.3", false, false}, {"vv1.2.3", false, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			if got := PinSet(tc.value); got != tc.pin {
				t.Errorf("PinSet(%q) = %v", tc.value, got)
			}
			if got := Helm(tc.value); got != tc.helm {
				t.Errorf("Helm(%q) = %v", tc.value, got)
			}
		})
	}
}
