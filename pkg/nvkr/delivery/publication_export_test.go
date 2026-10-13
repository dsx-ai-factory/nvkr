// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationExportUsesHeldBytesAfterSourceReplacement(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(map[bool]string{false: "binary", true: "disk"}[disk], func(t *testing.T) {
			request := releasePublicationRequestFixture(t, "linux", "amd64")
			if disk {
				request, _ = diskPublicationRequest(t, "arm64")
			}
			root := t.TempDir()
			source := filepath.Join(root, "publication")
			result, err := BuildPublicationRoot(t.Context(), source, request)
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), source, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			if err := os.Rename(source, filepath.Join(root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("substituted"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "exported")
			if err := publication.WriteLayout(t.Context(), out); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), out)
			if err != nil || inspection.RootDigest != result.RootDigest {
				t.Fatalf("export reopened substituted paths: %+v %v", inspection, err)
			}
		})
	}
}
