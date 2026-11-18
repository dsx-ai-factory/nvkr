// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFamilySnapshotRejectsChangedInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T)
		code   e.Code
	}{
		{"changed bytes", func(t *testing.T) { writeFixture(t, "inputs/policy.yaml", []byte("changed")) }, e.CodeWriteConflict},
		{"added file", func(t *testing.T) { writeFixture(t, "inputs/new.yaml", []byte("new")) }, e.CodeWriteConflict},
		{"added directory", func(t *testing.T) {
			if err := os.Mkdir("inputs/new", 0700); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"changed mode", func(t *testing.T) {
			if err := os.Chmod("inputs/policy.yaml", 0644); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for _, name := range []string{".github", "inputs"} {
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, "inputs/policy.yaml", []byte("original"))
			cfg := Config{Family: &FamilyPolicy{Catalogs: []FamilyCatalog{{Catalog: "inputs"}}}}
			snapshot, err := snapshotFamilyInputs(t.Context(), cfg, "inputs/policy.yaml")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(snapshot.root) })
			if err := snapshot.checkOriginals(t.Context()); err != nil {
				t.Fatal(err)
			}
			tc.change(t)
			assertCode(t, snapshot.checkOriginals(t.Context()), tc.code)
		})
	}
}

func TestFamilySnapshotRejectsSymlink(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{".github", "inputs"} {
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, "outside", []byte("outside"))
	if err := os.Symlink("../outside", "inputs/policy.yaml"); err != nil {
		t.Fatal(err)
	}
	_, err := snapshotFamilyInputs(t.Context(), Config{Family: &FamilyPolicy{}}, "inputs/policy.yaml")
	assertCode(t, err, e.CodeInvalidCatalog)
}

func TestFamilySnapshotPreservesModeUnderUmask(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("inputs", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(".github", 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, "inputs/policy.yaml", []byte("original"))
	if err := os.Chmod("inputs/policy.yaml", 0644); err != nil {
		t.Fatal(err)
	}
	prior := syscall.Umask(0077)
	defer syscall.Umask(prior)
	snapshot, err := snapshotFamilyInputs(t.Context(), Config{Family: &FamilyPolicy{}}, "inputs/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(snapshot.root) }()
	info, err := os.Stat(filepath.Join(snapshot.root, "inputs/policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("captured mode %o", info.Mode().Perm())
	}
}
