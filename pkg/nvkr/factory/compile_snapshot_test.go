// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const snapshotDefinition = "config/definition.json"
const snapshotEnvironment = "config/environment.json"

func snapshotFixture(t *testing.T) (string, authored.Definition, authored.Environment) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("bounded no-follow filesystem capture requires Linux or macOS")
	}
	root := t.TempDir()
	provider := testCatalogProvider(t)
	registry, err := provider.ReadFile(catalog.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := provider.ReadFile(catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("--- a/version.go\n+++ b/version.go\n@@ -1 +1 @@\n-old\n+new\n")
	declaration := fmt.Sprintf("  containerd:\n    patches:\n    - name: patches/version.patch\n      sha256: sha256:%x\n    artifactPath:", sha256.Sum256(patch))
	registry = []byte(strings.Replace(string(registry), "  containerd:\n    artifactPath:", declaration, 1))
	for _, name := range []string{"catalog", "override"} {
		writeSnapshotFixtureFile(t, root, name+"/registry.yaml", registry)
		writeSnapshotFixtureFile(t, root, name+"/pins.lock.yaml", lock)
		writeSnapshotFixtureFile(t, root, name+"/patches/version.patch", patch)
	}
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	replacement := base.Components["containerd"]
	replacement.ToolchainImage = "golang@sha256:" + strings.Repeat("a", 64)
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "capture-fixture",
		RequiresCatalogDigest: digest, Components: map[string]catalog.ComponentReplacement{"containerd": {Component: &replacement}}}
	if _, err := catalog.ApplyStack(base, overlay); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFixtureDocument(t, root, "config/overlay.json", overlay)
	definition, environment := fixtureInputs("snapshot-fixture", "tools", "")
	definition.Catalog = authored.Catalog{Base: "../catalog", Overlays: []string{"overlay.json"}}
	definition.Outputs = []authored.DefinitionOutput{
		{Name: "other", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"kubectl"}, Catalog: &authored.Catalog{Base: "../override"}},
		{Name: "tools", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd"}},
	}
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	writeSnapshotFixtureFile(t, root, "not-an-input.secret", []byte("never transport me"))
	return root, definition, environment
}

func writeSnapshotFixtureDocument(t *testing.T, root, name string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFixtureFile(t, root, name, raw)
}

func writeSnapshotFixtureFile(t *testing.T, root, name string, raw []byte) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureSnapshotFixture(t *testing.T) (string, *Plan, InputSnapshot) {
	t.Helper()
	root, _, _ := snapshotFixture(t)
	plan, snapshot, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	return root, plan, snapshot
}

func TestCompileSnapshotPreservesRelocatedInputIdentity(t *testing.T) {
	root, definition, environment := snapshotFixture(t)
	plan, snapshot, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	// Compare against the normal catalog loader and compiler using the original
	// logical document names, independent of the capture provider.
	base, err := loadFactoryCatalogStack(t.Context(), filepath.Join(root, snapshotDefinition), definition.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	override, err := loadFactoryCatalogStack(t.Context(), filepath.Join(root, snapshotDefinition), *definition.Outputs[0].Catalog)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := compileWithTargetStacks(t.Context(), definition, environment, base, map[string]*catalog.Stack{"other": override},
		"snapshot-test", snapshotDefinition, snapshotEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ordinary.Digest()
	if err != nil {
		t.Fatal(err)
	}
	capturedDigest, err := plan.Digest()
	if err != nil || capturedDigest != want {
		t.Fatalf("capture changed Plan identity: %s != %s (%v)", capturedDigest, want, err)
	}
	var files []string
	for _, file := range snapshot.Files {
		files = append(files, file.Path)
	}
	wantFiles := []string{"catalog/patches/version.patch", "catalog/pins.lock.yaml", "catalog/registry.yaml", snapshotDefinition,
		snapshotEnvironment, "config/overlay.json", "override/patches/version.patch", "override/pins.lock.yaml", "override/registry.yaml"}
	slices.Sort(wantFiles)
	if !slices.Equal(files, wantFiles) || len(snapshot.Directories) != 4 {
		t.Fatalf("captured closure differs: files=%v directories=%v", files, snapshot.Directories)
	}
	moved := filepath.Join(t.TempDir(), "relocated")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() (*Plan, error)
	}{
		{"memory replay after source removal", func() (*Plan, error) { return CompileSnapshot(t.Context(), snapshot, "snapshot-test") }},
		{"relocated capture", func() (*Plan, error) {
			got, _, err := CaptureCompileFiles(t.Context(), moved, snapshotDefinition, snapshotEnvironment, "snapshot-test")
			return got, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			digest, err := got.Digest()
			if err != nil || digest != want {
				t.Fatalf("Plan identity=%s, want %s (%v)", digest, want, err)
			}
		})
	}
}

func cloneSnapshot(t *testing.T, snapshot InputSnapshot) InputSnapshot {
	t.Helper()
	raw, err := canonical.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var copy InputSnapshot
	if err := canonical.UnmarshalExact(raw, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestCompileSnapshotRejectsIncompleteOrUnsafeClosure(t *testing.T) {
	_, _, original := captureSnapshotFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*InputSnapshot)
		code e.Code
	}{
		{"unknown schema", func(s *InputSnapshot) { s.SchemaVersion += "-unknown" }, e.CodeInvalidCatalog},
		{"absolute entry", func(s *InputSnapshot) { s.Files[0].Path = "/outside" }, e.CodeInvalidCatalog},
		{"traversal entry", func(s *InputSnapshot) { s.Files[0].Path = "../outside" }, e.CodeInvalidCatalog},
		{"backslash entry", func(s *InputSnapshot) { s.Files[0].Path = `catalog\outside` }, e.CodeInvalidCatalog},
		{"duplicate entry", func(s *InputSnapshot) { s.Files[1].Path = s.Files[0].Path }, e.CodeInvalidCatalog},
		{"unsorted entries", func(s *InputSnapshot) { s.Files[0], s.Files[1] = s.Files[1], s.Files[0] }, e.CodeInvalidCatalog},
		{"missing patch", func(s *InputSnapshot) { s.Files = s.Files[1:] }, e.CodeInvalidCatalog},
		{"corrupt patch", func(s *InputSnapshot) { s.Files[0].Data = []byte("substituted") }, e.CodeUnpinnedSource},
		{"missing directory fact", func(s *InputSnapshot) { s.Directories = s.Directories[1:] }, e.CodeInvalidCatalog},
		{"invented legacy contents", func(s *InputSnapshot) { s.Directories[0].Entries = []string{"legacy.yaml"} }, e.CodeInvalidCatalog},
		{"unknown authored field", func(s *InputSnapshot) {
			for i := range s.Files {
				if s.Files[i].Path == snapshotDefinition {
					s.Files[i].Data = append([]byte(`{"unknown":true,`), s.Files[i].Data[1:]...)
				}
			}
		}, e.CodeInvalidCatalog},
		{"extra file", func(s *InputSnapshot) {
			s.Files = append(s.Files, InputFile{Path: "zz-unused", Data: []byte("secret")})
		}, e.CodeInvalidCatalog},
		{"extra directory", func(s *InputSnapshot) {
			s.Directories = append(s.Directories, InputDirectory{Path: "zz-unused", Entries: []string{}})
		}, e.CodeInvalidCatalog},
		{"file ancestor", func(s *InputSnapshot) {
			s.Files = append([]InputFile{{Path: "catalog", Data: []byte("file")}}, s.Files...)
		}, e.CodeInvalidCatalog},
		{"directory hides file", func(s *InputSnapshot) {
			s.Directories = append([]InputDirectory{{Path: "catalog", Entries: []string{}}}, s.Directories...)
		}, e.CodeInvalidCatalog},
		{"oversized file", func(s *InputSnapshot) { s.Files[0].Data = make([]byte, catalog.MaxCatalogFileBytes+1) }, e.CodeInvalidCatalog},
		{"aggregate bytes", func(s *InputSnapshot) {
			s.Files = nil
			for i := range 9 {
				s.Files = append(s.Files, InputFile{Path: fmt.Sprintf("file-%02d", i), Data: make([]byte, catalog.MaxCatalogFileBytes)})
			}
		}, e.CodeInvalidCatalog},
		{"entry limit", func(s *InputSnapshot) { s.Files = make([]InputFile, catalog.MaxCatalogDirectoryEntries+1) }, e.CodeInvalidCatalog},
		{"depth limit", func(s *InputSnapshot) {
			s.Files[0].Path = strings.Repeat("a/", catalog.MaxCatalogTraversalDepth+1) + "file"
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := cloneSnapshot(t, original)
			tc.edit(&snapshot)
			got, err := CompileSnapshot(t.Context(), snapshot, "snapshot-test")
			assertFactoryErrorCode(t, err, tc.code)
			if got != nil {
				t.Fatal("invalid snapshot returned a partial Plan")
			}
		})
	}
}

func TestCaptureCompileFilesRejectsUnsafeSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string, *authored.Definition)
	}{
		{"absolute catalog", func(_ *testing.T, root string, d *authored.Definition) {
			d.Catalog.Base = filepath.Join(root, "catalog")
		}},
		{"escaping catalog", func(_ *testing.T, _ string, d *authored.Definition) { d.Catalog.Base = "../../outside" }},
		{"escaping overlay", func(_ *testing.T, _ string, d *authored.Definition) { d.Catalog.Overlays = []string{"../../outside"} }},
		{"symlink file", func(t *testing.T, root string, _ *authored.Definition) {
			source := filepath.Join(root, "catalog/registry.yaml")
			if err := os.Rename(source, source+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source+".saved", source); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink directory", func(t *testing.T, root string, _ *authored.Definition) {
			source := filepath.Join(root, "catalog")
			if err := os.Rename(source, source+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source+"-saved", source); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, definition, _ := snapshotFixture(t)
			tc.edit(t, root, &definition)
			writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
			plan, snapshot, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			if plan != nil || snapshot.SchemaVersion != "" {
				t.Fatal("failed capture returned partial data")
			}
		})
	}
}

func TestCompileSnapshotContextAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil", nil, e.CodeUsage},
		{"canceled", canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileSnapshot(tc.ctx, InputSnapshot{}, "test")
			assertFactoryErrorCode(t, err, tc.code)
			_, _, err = CaptureCompileFiles(tc.ctx, "/must-not-open", snapshotDefinition, snapshotEnvironment, "test")
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}
