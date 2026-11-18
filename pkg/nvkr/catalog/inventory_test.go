// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInventoryMaintainedCatalogsOffline(t *testing.T) {
	// Pin-set catalog loading normally invokes Git. Inventory must work without
	// any executable available, including when the Git cache is empty.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	entries, err := InventoryContext(t.Context(), "../../../inputs/catalogs")
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual []string
	if err := filepath.WalkDir("../../../inputs/catalogs", func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == RegistryFile {
			relative, err := filepath.Rel("../../../inputs/catalogs", filepath.Dir(file))
			if err != nil {
				return err
			}
			expected = append(expected, filepath.ToSlash(relative))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		actual = append(actual, entry.Path)
	}
	slices.Sort(expected)
	if len(entries) == 0 || !slices.Equal(expected, actual) {
		t.Fatalf("missing catalog locations: want %v, got %v", expected, actual)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Path >= entries[i].Path {
			t.Fatal("unordered catalog paths")
		}
	}
	for _, name := range []string{"nvkectl", "kubectl"} {
		t.Run(name, func(t *testing.T) {
			found, err := FilterInventory(entries, name, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(found) < 2 {
				t.Fatalf("want duplicate locations, got %d", len(found))
			}
			for _, entry := range found {
				component := entry.Components[0]
				if component.Source == nil && component.Acquisition == nil {
					t.Fatalf("missing recorded pin in %s", entry.Path)
				}
			}
		})
	}
	again, err := InventoryContext(t.Context(), "../../../inputs/catalogs")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(RenderInventoryIndex(entries), RenderInventoryIndex(again)) {
		t.Fatal("index is nondeterministic")
	}
}

func TestInventoryRootAdmission(t *testing.T) {
	dir, _, _, _ := fragmentFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(filepath.Dir(dir)), alias); err != nil {
		t.Fatal(err)
	}
	// A symlink in an ancestor is supported, just as for ordinary leaf loading.
	// Keep the fragment's own parent below that ancestor: its independent
	// no-follow boundary must still reject a symlink as the fragment root.
	entries, err := InventoryContext(t.Context(), filepath.Join(alias, filepath.Base(filepath.Dir(dir)), filepath.Base(dir)))
	if err != nil || len(entries) != 1 || entries[0].Path != "." {
		t.Fatalf("symlinked ancestor: %v, %v", entries, err)
	}
	for _, root := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing"), alias} {
		entries, err := InventoryContext(t.Context(), root)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog || entries != nil {
			t.Fatalf("root %q: got %v, %v", root, entries, err)
		}
	}
}

func TestInventoryIncludesImportedNamesAndSeparateLocations(t *testing.T) {
	dir, reg, _, _ := fragmentFixture(t)
	reg.Imports[0].Components["renamed-kubectl"] = reg.Imports[0].Components["kubectl"]
	delete(reg.Imports[0].Components, "kubectl")
	writeFragmentRegistry(t, dir, reg)
	other := filepath.Join(filepath.Dir(dir), "second")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{RegistryFile, LockFile} {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		writeFragmentFixture(t, filepath.Join(other, file), raw)
	}
	entries, err := InventoryContext(t.Context(), filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	found, err := FilterInventory(entries, "renamed-kubectl", "", "")
	if err != nil || len(found) != 2 {
		t.Fatalf("two imported locations: %+v, %v", found, err)
	}
	if found[0].Path != "consumer" || found[1].Path != "second" || !reflect.DeepEqual(found[0].Components, found[1].Components) {
		t.Fatal("lost duplicate location or import")
	}
}

func TestInventoryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string, *registryDoc)
		code   e.Code
	}{
		{"missing lock", func(t *testing.T, dir string, _ *registryDoc) {
			if err := os.Rename(filepath.Join(dir, LockFile), filepath.Join(dir, "saved-lock")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeUnpinnedSource},
		{"invalid import", func(t *testing.T, dir string, reg *registryDoc) {
			reg.Imports[0].SHA256 = strings.Repeat("0", 64)
			writeFragmentRegistry(t, dir, reg)
		}, e.CodeInvalidCatalog},
		{"symlink", func(t *testing.T, dir string, _ *registryDoc) {
			if err := os.Symlink(RegistryFile, filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, reg, _, _ := fragmentFixture(t)
			tc.mutate(t, dir, reg)
			entries, err := InventoryContext(t.Context(), filepath.Dir(dir))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || entries != nil {
				t.Fatalf("want %s and no partial output, got %+v, %v", tc.code, entries, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := InventoryContext(ctx, t.TempDir())
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("cancellation: %v", err)
	}
	_, err = FilterInventory([]InventoryCatalog{{Path: "one"}}, "absent", "", "")
	if !errors.As(err, &coded) || coded.Code != e.CodeNoMatch {
		t.Fatalf("unknown component: %v", err)
	}
}

func TestInventoryIndexEscapesAuthoredText(t *testing.T) {
	raw := RenderInventoryIndex([]InventoryCatalog{{Path: "x|<script>\n", Components: []ComponentDescription{{Name: "`\x1b*_[]", Mode: "excluded"}}}})
	if bytes.Contains(raw, []byte("<script>")) || bytes.Contains(raw, []byte{27}) || !bytes.Contains(raw, []byte("&#124;")) {
		t.Fatalf("unsafe markdown: %s", raw)
	}
	for _, escaped := range []string{"&#92;", "&#42;", "&#95;", "&#91;"} {
		if !bytes.Contains(raw, []byte(escaped)) {
			t.Fatalf("missing Markdown escape %s: %s", escaped, raw)
		}
	}
}

func TestInventoryIndexUnaffectedByPinRefresh(t *testing.T) {
	dir, _, _, _ := fragmentFixture(t)
	entries, err := InventoryContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	before := RenderInventoryIndex(entries)
	changed := 0
	for i := range entries {
		for j := range entries[i].Components {
			if source := entries[i].Components[j].Source; source != nil {
				source.Commit = strings.Repeat("f", 40)
				source.Tag = "v99.0.0"
				changed++
			}
		}
	}
	if changed == 0 || !bytes.Equal(before, RenderInventoryIndex(entries)) {
		t.Fatal("pin-only refresh must not stale the navigation index")
	}
}
