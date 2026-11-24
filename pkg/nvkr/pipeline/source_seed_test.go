// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceSeedGitObjectsRefuseLinksAndAlternateMetadata(t *testing.T) {
	for _, scenario := range []string{"regular entries", "symlink", "nested symlink", "alternate metadata", "entry bound", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "pack"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "object"), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			want := e.Code("")
			entries := 0
			ctx := t.Context()
			switch scenario {
			case "symlink", "nested symlink":
				name, target := "link", "object"
				if scenario == "nested symlink" {
					name, target = "pack/link", "../object"
				}
				if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
				want = e.CodeFetchFailed
			case "alternate metadata":
				if err := os.Mkdir(filepath.Join(root, "info"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "info/alternates"), []byte("synthetic-path"), 0600); err != nil {
					t.Fatal(err)
				}
				want = e.CodeFetchFailed
			case "entry bound":
				entries, want = maxSourceClosureEntries, e.CodeFetchFailed
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = e.CodeTimeout
			}
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			if err := admitSeedGitObjects(ctx, directory, "", &entries); code(err) != want {
				t.Fatalf("object store admission = %v, want %s", err, want)
			}
		})
	}
}
