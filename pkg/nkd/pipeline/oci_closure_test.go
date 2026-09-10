package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestOCIClosureRejectsMalformedDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ociManifestDocument)
	}{
		{"wrong schema", func(m *ociManifestDocument) { m.SchemaVersion = 1 }},
		{"wrong manifest media", func(m *ociManifestDocument) { m.MediaType = "application/unknown" }},
		{"unknown config media", func(m *ociManifestDocument) { m.Config.MediaType = "application/unknown" }},
		{"invalid config digest", func(m *ociManifestDocument) { m.Config.Digest = "sha256:bad" }},
		{"missing config blob", func(m *ociManifestDocument) { m.Config.Digest = "sha256:" + strings.Repeat("a", 64) }},
		{"negative size", func(m *ociManifestDocument) { m.Config.Size = -1 }},
		{"wrong layer size", func(m *ociManifestDocument) { m.Layers[0].Size++ }},
		{"unknown layer media", func(m *ociManifestDocument) { m.Layers[0].MediaType = "application/unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "layout")
			oldDigest := writeMinimalOCILayout(t, root)
			oldPath := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(oldDigest, "sha256:"))
			raw, err := os.ReadFile(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest ociManifestDocument
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			tc.change(&manifest)
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			digest := digestBytes(raw)
			if err := os.Remove(oldPath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			index, err := json.Marshal(ociIndexDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []ociDescriptor{{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digest, Size: int64(len(raw))}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "index.json"), index, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := verifyOCILayout(t.Context(), root, digest); code(err) != e.CodeVerifyFailed {
				t.Fatalf("malformed closure accepted: %v", err)
			}
		})
	}
}

func TestOCIClosureAcceptsNestedIndexesWithSharedDescriptors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "layout")
	writeMinimalOCILayout(t, root)
	indexPath := filepath.Join(root, "index.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index ociIndexDocument
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	index.Manifests = append(index.Manifests, index.Manifests[0])
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	nestedDigest := digestBytes(raw)
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(nestedDigest, "sha256:")), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	outer, err := json.Marshal(ociIndexDocument{SchemaVersion: 2, MediaType: index.MediaType, Manifests: []ociDescriptor{{MediaType: index.MediaType, Digest: nestedDigest, Size: int64(len(raw))}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, outer, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{nestedDigest, digestBytes(outer)} {
		if err := verifyOCILayout(t.Context(), root, want); err != nil {
			t.Fatalf("nested/shared closure rejected: %v", err)
		}
	}
}

func TestOCIJSONRejectsAmbiguousAndTrailingDocuments(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"nested":[{"x":1,"x":2}]}`, `{} {}`, `{"x":`, `[{]`, `{"x" 1}`, `{"x":[1,]}`} {
		t.Run(raw, func(t *testing.T) {
			var decoded any
			if err := decodeOCIJSON([]byte(raw), &decoded); err == nil {
				t.Fatalf("ambiguous JSON accepted: %s", raw)
			}
		})
	}
}
