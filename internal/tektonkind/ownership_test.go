//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tektonkind

import "testing"

func TestKeyGenerationUser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []string
		want    string
	}{
		{"rootful", nil, "1001:101"},
		{"other security options", []string{"name=seccomp,profile=builtin", "name=cgroupns"}, "1001:101"},
		{"rootless", []string{"name=seccomp,profile=builtin", "name=rootless"}, "0:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyGenerationUser(tc.options, 1001, 101); got != tc.want {
				t.Fatalf("key generation user = %q, want %q", got, tc.want)
			}
		})
	}
}
