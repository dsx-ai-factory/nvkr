// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func checkLocalEvidenceBundleFixture(t *testing.T, owner *localVerifiedQualification) {
	t.Helper()
	for _, mode := range []string{"exact", "existing", "canceled", "linked-parent", "linked-file", "changed-bytes"} {
		t.Run("durable evidence/"+mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "evidence.json")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "existing":
				if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "linked-parent":
				link := filepath.Join(root, "link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, "evidence.json")
			}
			published, err := owner.writeEvidenceBundle(ctx, path)
			if mode == "existing" || mode == "canceled" || mode == "linked-parent" {
				code := e.CodeImmutableViolation
				if mode == "canceled" {
					code = e.CodeTimeout
				}
				assertCode(t, err, code)
				if published {
					t.Fatal("rejected export published")
				}
				if mode == "existing" {
					raw, err := os.ReadFile(path)
					if err != nil || string(raw) != "preserve" {
						t.Fatal("existing bundle changed", err)
					}
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "evidence.json" && entry.Name() != "link" {
						t.Fatal("private stage leaked", entry.Name())
					}
				}
				return
			}
			if err != nil || !published {
				t.Fatalf("export: published=%v, err=%v", published, err)
			}
			if mode == "linked-file" {
				link := filepath.Join(root, "linked.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			if mode == "changed-bytes" {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			files, err := readLocalEvidenceBundle(t.Context(), path)
			if mode == "linked-file" || mode == "changed-bytes" {
				if err == nil || files != nil {
					t.Fatal("invalid bundle accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected, err := owner.evidenceFiles(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != len(expected) {
				t.Fatal("incomplete bundle")
			}
			for name, raw := range expected {
				if !bytes.Equal(raw, files[name]) {
					t.Fatal("changed bundle member", name)
				}
			}
			second := filepath.Join(root, "second.json")
			if _, err := owner.writeEvidenceBundle(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			firstBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			secondBytes, err := os.ReadFile(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("bundle is not deterministic")
			}
		})
	}
}
