// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			out, diagnostic, status := runNVKEOSCatalogResolver(t, candidate)
			if tc.failure != "" {
				if status != 1 || out != "" || !strings.Contains(diagnostic, "[NVKR-E-INVALID-CATALOG] "+tc.failure) {
					t.Fatalf("status=%d stdout=%q stderr=%q", status, out, diagnostic)
				}
				if tc.name == "ambiguous" {
					for _, registry := range tc.registries {
						if !strings.Contains(diagnostic, filepath.Join(base, registry)) {
							t.Fatalf("missing matched registry %q in %q", registry, diagnostic)
						}
					}
				}
				return
			}
			if want := filepath.Join(base, tc.want) + "\n"; status != 0 || out != want {
				t.Fatalf("status=%d output=%q, want %q", status, out, want)
			}
		})
	}
}

func TestNVKEOSCandidateCatalogInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		directory bool
	}{
		{name: "missing argument"},
		{name: "empty argument", args: []string{""}},
		{name: "extra argument", args: []string{"candidate", "extra"}},
		{name: "directory at registry.yaml", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			want := "expected one candidate checkout path"
			if tc.directory {
				candidate := t.TempDir()
				for _, layout := range []string{"node-images", "node-images/diagnostic"} {
					if err := os.MkdirAll(filepath.Join(candidate, "inputs/catalogs", layout, "registry.yaml"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				args = []string{candidate}
				want = "missing supported NVKE OS catalog registry in candidate checkout"
			}
			out, diagnostic, status := runNVKEOSCatalogResolver(t, args...)
			if status != 1 || out != "" || diagnostic != "[NVKR-E-INVALID-CATALOG] "+want+"\n" {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, out, diagnostic)
			}
		})
	}
}

func runNVKEOSCatalogResolver(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{"nvke-os-catalog-dir.sh"}, args...)...)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	status := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return stdout.String(), stderr.String(), status
}
