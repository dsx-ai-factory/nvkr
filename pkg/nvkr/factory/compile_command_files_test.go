// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestCompileSnapshotCapturesCommandFiles(t *testing.T) {
	for _, fragmentSource := range []bool{false, true} {
		t.Run(fmt.Sprint("fragment=", fragmentSource), func(t *testing.T) {
			root, _, _ := snapshotFixture(t)
			before, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, "catalog/registry.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var registry map[string]any
			if err := yaml.UnmarshalStrict(raw, &registry); err != nil {
				t.Fatal(err)
			}
			components := registry["components"].(map[string]any)
			build := components["containerd"].(map[string]any)["buildCommand"].([]any)
			buildBody := []byte(build[0].(string))
			build[0] = map[string]string{"file": "scripts/build"}
			sources := registry["sources"].(map[string]any)
			vendor := sources["etcd"].(map[string]any)["vendorCommand"].([]any)
			vendorBody := []byte(vendor[2].(string))
			vendor[2] = map[string]string{"file": "scripts/vendor"}
			vendorPath := "catalog/scripts/vendor"
			if fragmentSource {
				fragment, err := yaml.Marshal(map[string]any{"apiVersion": "nvkr.dev/catalog-fragment/v1", "kind": "CatalogFragment", "sources": sources})
				if err != nil {
					t.Fatal(err)
				}
				selected := map[string]string{}
				for name := range sources {
					selected[name] = name
				}
				delete(registry, "sources")
				registry["imports"] = []any{map[string]any{"path": "../shared/sources.yaml", "sha256": fmt.Sprintf("%x", sha256.Sum256(fragment)), "sources": selected}}
				writeSnapshotFixtureFile(t, root, "shared/sources.yaml", fragment)
				vendorPath = "shared/scripts/vendor"
			}
			writeSnapshotFixtureDocument(t, root, "catalog/registry.yaml", registry)
			writeSnapshotFixtureFile(t, root, "catalog/scripts/build", buildBody)
			writeSnapshotFixtureFile(t, root, vendorPath, vendorBody)
			after, snapshot, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
			if err != nil {
				t.Fatal(err)
			}
			want, err := before.Digest()
			if err != nil {
				t.Fatal(err)
			}
			got, err := after.Digest()
			if err != nil || got != want {
				t.Fatalf("command files changed Plan identity: %s / %s: %v", got, want, err)
			}
			for name, body := range map[string][]byte{"catalog/scripts/build": buildBody, vendorPath: vendorBody} {
				found := false
				for _, file := range snapshot.Files {
					if file.Path == name {
						found = bytes.Equal(file.Data, body)
					}
				}
				if !found {
					t.Fatalf("snapshot omitted exact bytes of %s", name)
				}
				writeSnapshotFixtureFile(t, root, name, []byte("changed working file"))
			}
			replay, err := CompileSnapshot(t.Context(), snapshot, "snapshot-test")
			if err != nil {
				t.Fatal(err)
			}
			got, err = replay.Digest()
			if err != nil || got != want {
				t.Fatalf("replay read working file: %v", err)
			}
			for _, missing := range []string{"catalog/scripts/build", vendorPath} {
				incomplete := snapshot
				incomplete.Files = slices.DeleteFunc(slices.Clone(snapshot.Files), func(f InputFile) bool { return f.Path == missing })
				_, err := CompileSnapshot(t.Context(), incomplete, "snapshot-test")
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}
