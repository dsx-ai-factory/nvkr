// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func legacyPublicationBootstrapFixture(t *testing.T, root string) string {
	t.Helper()
	index, manifest := fixtureManifest(t, root)
	original := index.Manifests[0]
	manifest.ArtifactType = manifest.artifactType()
	delete(manifest.Annotations, artifactTypeAnnotation)
	index.ArtifactType = index.artifactType()
	delete(index.Annotations, artifactTypeAnnotation)
	writer := layoutWriter{root: root}
	descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.ArtifactType, descriptor.Annotations = original.ArtifactType, original.Annotations
	index.Manifests[0] = descriptor
	pin, err := writer.writeIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest != original.Digest {
		if err := os.Remove(fixtureBlobPath(root, original.Digest)); err != nil {
			t.Fatal(err)
		}
	}
	return pin
}

func TestMetadataExportPreservesRetainedBootstrapBytes(t *testing.T) {
	for _, representation := range []string{"current", "legacy", "legacy with whitespace"} {
		t.Run(representation, func(t *testing.T) {
			full, root := releasePublicationFixture(t)
			if representation != "current" {
				root = legacyPublicationBootstrapFixture(t, full)
			}
			indexPath := filepath.Join(full, "index.json")
			want, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if representation == "legacy with whitespace" {
				want = append(want, '\n')
				root = digestBytes(want)
				if err := os.WriteFile(indexPath, want, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(t.TempDir(), "metadata")
			exported, err := ExportMetadataWithExpectedRoot(t.Context(), full, out, root)
			if err != nil || exported.RootDigest != root {
				t.Fatalf("retained root changed: %+v %v", exported, err)
			}
			got, err := os.ReadFile(filepath.Join(out, "index.json"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("retained bootstrap bytes changed: %v", err)
			}
		})
	}
}

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
	_, err = buildMetadataRoot(t.Context(), out, inspection.Root, sources, other, "")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("mismatched built metadata committed: %v", err)
	}
}
