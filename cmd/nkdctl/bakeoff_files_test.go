// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBakeOffCLIComparesFilesWithoutClaimingQualification(t *testing.T) {
	for _, tc := range []struct {
		name, second string
		code         e.Code
	}{
		{"identical", "a", ""},
		{"changed artifact", "b", e.CodeVerifyFailed},
		{"missing first", "missing-first", e.CodeVerifyFailed},
		{"missing second", "missing-second", e.CodeVerifyFailed},
		{"malformed record", "malformed", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{filepath.Join(t.TempDir(), "a.json"), filepath.Join(t.TempDir(), "b.json")}
			for i, path := range paths {
				if tc.second == "missing-first" && i == 0 || tc.second == "missing-second" && i == 1 {
					continue
				}
				digest := "a"
				if i == 1 && tc.second == "b" {
					digest = "b"
				}
				manifest, err := diagnosticManifestFromReport(diagnosticBakeOffContext(t, "--platform", "linux/amd64"), diagnosticBakeOffReport(digest))
				if err != nil {
					t.Fatal(err)
				}
				raw, err := manifest.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if i == 1 && tc.second == "malformed" {
					raw = []byte("{")
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output, err := invokeLifecycleCLI(t, "bake-off", "--compare", paths[0], "--compare", paths[1])
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"UNQUALIFIED DIAGNOSTIC", "bit-identical", "not validation evidence"} {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
		})
	}
}
