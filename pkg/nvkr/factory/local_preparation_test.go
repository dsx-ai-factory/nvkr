// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrepareLocalInputsCaptureCompileReplay(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "relative inputs"
		if absolute {
			name = "absolute inputs inside repository"
		}
		t.Run(name, func(t *testing.T) {
			root, definition, environment := snapshotFixture(t)
			t.Chdir(root)
			definitionPath, environmentPath := snapshotDefinition, snapshotEnvironment
			if absolute {
				definitionPath, environmentPath = filepath.Join(root, definitionPath), filepath.Join(root, environmentPath)
				definition.Catalog.Base = filepath.Join(root, "catalog")
				definition.Catalog.Overlays = []string{filepath.Join(root, "config/overlay.json")}
				definition.Outputs[0].Catalog.Base = filepath.Join(root, "override")
				writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
			}
			original, err := CompileDocuments(t.Context(), snapshotDefinition, definition, snapshotEnvironment, environment, "preparation-test")
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareLocalInputs(t.Context(), definitionPath, environmentPath, ".nvkr/prepared", map[string]any{},
				func(*authored.Environment, string) error { return nil },
				func(_ authored.DefinitionOutput, name string, component catalog.Component) (catalog.Component, bool, error) {
					// Emit a real predecessor-pinned overlay without changing producer identity.
					return component, name == "containerd", nil
				})
			if err != nil {
				t.Fatal(err)
			}
			derived, err := authored.LoadDefinition(t.Context(), prepared.Definition)
			if err != nil {
				t.Fatal(err)
			}
			derivedEnvironment, err := authored.LoadEnvironment(t.Context(), prepared.Environment)
			if err != nil {
				t.Fatal(err)
			}
			ordinary, err := CompileDocuments(t.Context(), prepared.Definition, derived, prepared.Environment, derivedEnvironment, "preparation-test")
			if err != nil {
				t.Fatal(err)
			}
			if len(original.Builds) != len(ordinary.Builds) {
				t.Fatal("relocation changed the selected builds")
			}
			for i, build := range original.Builds {
				if build.Name != ordinary.Builds[i].Name || build.BuildDigest != ordinary.Builds[i].BuildDigest {
					t.Fatalf("relocation changed producer/source/toolchain identity for %q", build.Name)
				}
			}
			captured, snapshot, err := CaptureCompileFiles(t.Context(), root, prepared.Definition, prepared.Environment, "preparation-test")
			if err != nil {
				t.Fatal(err)
			}
			var files []string
			for _, file := range snapshot.Files {
				files = append(files, file.Path)
			}
			for _, expected := range []string{".nvkr/prepared/catalog-tools.json", "config/overlay.json", "catalog/registry.yaml", "override/registry.yaml"} {
				if !slices.Contains(files, expected) {
					t.Fatalf("prepared capture omitted %q: %v", expected, files)
				}
			}
			// Replay must still work after the original physical inputs disappear.
			if err := os.Rename(root, filepath.Join(t.TempDir(), "moved")); err != nil {
				t.Fatal(err)
			}
			replayed, err := CompileSnapshot(t.Context(), snapshot, "preparation-test")
			if err != nil {
				t.Fatal(err)
			}
			want, err := ordinary.Digest()
			if err != nil {
				t.Fatal(err)
			}
			for _, plan := range []*Plan{captured, replayed} {
				got, err := plan.Digest()
				if err != nil || got != want {
					t.Fatalf("capture/replay changed prepared Plan identity: %s != %s (%v)", got, want, err)
				}
			}
		})
	}
}

func TestPrepareLocalInputsRejectsOutsideCapturedRootBeforeOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testing.T, string, string, *authored.Definition)
	}{
		{"absolute base", func(_ *testing.T, _, outside string, d *authored.Definition) {
			d.Catalog.Base = filepath.Join(outside, "catalog")
		}},
		{"relative base", func(t *testing.T, root, outside string, d *authored.Definition) {
			base, err := filepath.Rel(filepath.Join(root, "config"), filepath.Join(outside, "catalog"))
			if err != nil {
				t.Fatal(err)
			}
			d.Catalog.Base = filepath.ToSlash(base)
		}},
		{"authored overlay", func(_ *testing.T, _, outside string, d *authored.Definition) {
			d.Catalog.Overlays = []string{filepath.Join(outside, "config/overlay.json")}
		}},
		{"output base", func(_ *testing.T, _, outside string, d *authored.Definition) {
			d.Outputs[0].Catalog.Base = filepath.Join(outside, "override")
		}},
		{"output overlay", func(_ *testing.T, _, outside string, d *authored.Definition) {
			d.Outputs[0].Catalog.Overlays = []string{filepath.Join(outside, "config/overlay.json")}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, definition, _ := snapshotFixture(t)
			outside, _, _ := snapshotFixture(t)
			t.Chdir(root)
			test.edit(t, root, outside, &definition)
			writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
			_, err := prepareLocalInputs(t.Context(), snapshotDefinition, snapshotEnvironment, ".nvkr/rejected", map[string]any{},
				func(*authored.Environment, string) error { return nil },
				func(_ authored.DefinitionOutput, _ string, component catalog.Component) (catalog.Component, bool, error) {
					return component, true, nil
				})
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			if _, err := os.Lstat(".nvkr"); !os.IsNotExist(err) {
				t.Fatal("rejected reference created preparation output")
			}
		})
	}
}
