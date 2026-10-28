// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMetadataExportRequiresExactPublishedRootBeforeCommit(t *testing.T) {
	full, root := releasePublicationFixture(t)
	for _, tc := range []struct {
		name, expected string
		code           e.Code
	}{
		{"matching root", root, ""},
		{"another root", "sha256:" + strings.Repeat("f", 64), e.CodeVerifyFailed},
		{"malformed root", "not-a-digest", e.CodeUsage},
		{"missing root", "", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "metadata")
			result, err := ExportMetadataWithExpectedRoot(t.Context(), full, out, tc.expected)
			if tc.code != "" {
				assertRegistryCode(t, err, tc.code)
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatalf("failed export committed destination: %v", err)
				}
				return
			}
			if err != nil || result.RootDigest != root {
				t.Fatalf("bound export = %+v, %v", result, err)
			}
			inspection, err := InspectMetadata(t.Context(), out)
			if err != nil || inspection.RootDigest != root {
				t.Fatalf("exported root = %+v, %v", inspection, err)
			}
		})
	}
}

func TestMetadataBuilderRejectsExpectedRootMismatchBeforeCommit(t *testing.T) {
	full, root := releasePublicationFixture(t)
	inspection, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, object := range inspection.Root.Objects {
		sources[object.Name] = full
	}
	other := "sha256:" + strings.Repeat("f", 64)
	if other == root {
		t.Fatal("fixture unexpectedly equals mismatch")
	}
	out := filepath.Join(t.TempDir(), "metadata")
	_, err = buildMetadataRoot(t.Context(), out, inspection.Root, sources, other)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("mismatched built metadata committed: %v", err)
	}
}
