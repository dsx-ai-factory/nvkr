// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNVKEOSCandidateCatalogLayout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registries []string
		want       string
		failure    string
	}{
		{"consolidated", []string{"registry.yaml"}, "", ""},
		{"pre-consolidation", []string{"diagnostic/registry.yaml"}, "diagnostic", ""},
		{"missing", nil, "", "missing supported"},
		{"ambiguous", []string{"registry.yaml", "diagnostic/registry.yaml"}, "", "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "candidate with spaces")
			base := filepath.Join(candidate, "inputs/catalogs/node-images")
			for _, registry := range tc.registries {
				swapWrite(t, filepath.Join(base, registry), "fixture registry\n")
			}
			out, status := swapRun(t, "", "bash", "nvke-os-catalog-dir.sh", candidate)
			if tc.failure != "" {
				if status != 1 || !strings.Contains(out, "[NVKR-E-INVALID-CATALOG] "+tc.failure) {
					t.Fatalf("status=%d output=%q", status, out)
				}
				return
			}
			if want := filepath.Join(base, tc.want) + "\n"; status != 0 || out != want {
				t.Fatalf("status=%d output=%q, want %q", status, out, want)
			}
		})
	}
}
