// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contextDirProvider(t *testing.T, root string) ContextDataProvider {
	t.Helper()
	provider, ok := Dir(root).(ContextDataProvider)
	if !ok {
		t.Fatal("directory provider does not implement ContextDataProvider")
	}
	return provider
}

func TestDirProviderRejectsOversizedAndSymlinkFiles(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, RegistryFile)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(MaxCatalogFileBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := contextDirProvider(t, root).ReadFileContext(context.Background(), RegistryFile); err == nil || !strings.Contains(err.Error(), "bound") {
			t.Fatalf("oversized catalog file returned %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "registry.yaml")
		if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, RegistryFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := contextDirProvider(t, root).ReadFileContext(context.Background(), RegistryFile); err == nil {
			t.Fatal("symlink catalog file was followed")
		}
	})

	t.Run("symlink-root", func(t *testing.T) {
		realRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(realRoot, RegistryFile), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "catalog")
		if err := os.Symlink(realRoot, link); err != nil {
			t.Fatal(err)
		}
		if _, err := contextDirProvider(t, link).ReadFileContext(context.Background(), RegistryFile); err == nil {
			t.Fatal("symlink catalog root was followed")
		}
	})
}

func TestDirProviderBoundsAndSortsRecursiveListing(t *testing.T) {
	t.Run("sorted", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"z.yaml", "nested/b.yaml", "a.yaml"} {
			path := filepath.Join(root, "overlays", filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := contextDirProvider(t, root).ReadDirContext(context.Background(), "overlays")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a.yaml", "nested/b.yaml", "z.yaml"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("listing = %v, want %v", got, want)
		}
	})

	t.Run("entry-count", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a.yaml", "b.yaml"} {
			if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			name    string
			entries int
			wantErr bool
		}{
			{"last admitted entries", MaxCatalogDirectoryEntries - 2, false},
			{"first extra entry", MaxCatalogDirectoryEntries - 1, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				directory, err := os.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				defer directory.Close()
				// Exercise the aggregate boundary through the real walker without
				// creating the already-accounted-for thousand directory entries.
				state := &catalogWalkState{entries: tc.entries}
				err = walkCatalogDirectory(t.Context(), directory, "", 0, state)
				if tc.wantErr {
					if err == nil || !strings.Contains(err.Error(), "entry bound") || len(state.files) != 0 {
						t.Fatalf("oversized directory returned files=%v, err=%v", state.files, err)
					}
				} else if err != nil || state.entries != MaxCatalogDirectoryEntries || len(state.files) != 2 {
					t.Fatalf("last admitted entries returned state=%+v, err=%v", state, err)
				}
			})
		}
	})

	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "overlays")
		for index := 0; index <= MaxCatalogTraversalDepth; index++ {
			deep = filepath.Join(deep, "d")
		}
		if err := os.MkdirAll(deep, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(deep, "leaf.yaml"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextDirProvider(t, root).ReadDirContext(context.Background(), "overlays"); err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatalf("deep directory returned %v", err)
		}
	})

	t.Run("nested-symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "overlays"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "overlays", "escape")); err != nil {
			t.Fatal(err)
		}
		if _, err := contextDirProvider(t, root).ReadDirContext(context.Background(), "overlays"); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("nested symlink returned %v", err)
		}
	})
}
