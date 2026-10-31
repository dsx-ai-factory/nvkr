// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The digests AICR v1.1.2 reported for its own push of the fixture.
const (
	aicrFixtureManifest = "sha256:7ecb2e63700525a9052c9ade020d9e054f195f2f4c77478b7130623bf152ee8f"
	aicrFixtureLayer    = "sha256:b923757f1f7fa8782e676b33b817723fb51b7ac01ef6c7be2111baa791d7ed1d"
)

func aicrLayoutFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "aicr")
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "aicr-v1.1.2-helm"))); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFixtureBlob(t *testing.T, root string, raw []byte) string {
	t.Helper()
	digest := digestBytes(raw)
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
}

func removeFixtureBlob(t *testing.T, root, digest string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureIndex(t *testing.T, root string, manifests ...Descriptor) {
	t.Helper()
	raw, err := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: manifests})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// rewriteFixtureManifest edits the producer manifest and returns its new digest.
func rewriteFixtureManifest(t *testing.T, root string, edit func(map[string]any)) string {
	t.Helper()
	path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(aicrFixtureManifest, "sha256:"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	edit(manifest)
	if raw, err = json.Marshal(manifest); err != nil {
		t.Fatal(err)
	}
	removeFixtureBlob(t, root, aicrFixtureManifest)
	digest := writeFixtureBlob(t, root, raw)
	writeFixtureIndex(t, root, Descriptor{MediaType: MediaOCIManifest, Digest: digest, Size: int64(len(raw))})
	return digest
}

func fixtureLayers(manifest map[string]any) []any { return manifest["layers"].([]any) }

func replaceFixtureLayer(t *testing.T, root string, raw []byte) string {
	t.Helper()
	digest := writeFixtureBlob(t, root, raw)
	removeFixtureBlob(t, root, aicrFixtureLayer)
	return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
		layer := fixtureLayers(manifest)[0].(map[string]any)
		layer["digest"], layer["size"] = digest, len(raw)
	})
}

func symlinkArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportBundleLayoutPreservesProducerDigest(t *testing.T) {
	source := aicrLayoutFixture(t)
	out := filepath.Join(t.TempDir(), "imported")
	result, err := ImportBundleLayout(t.Context(), out, source, aicrFixtureManifest)
	if err != nil {
		t.Fatal(err)
	}
	if result.LayoutPath != out || result.SemanticDigest != aicrFixtureLayer {
		t.Fatalf("unexpected import result: %+v", result)
	}
	descriptor, layer, err := ReadArtifact(t.Context(), out)
	if err != nil || descriptor.Digest != aicrFixtureManifest || descriptor.ArtifactType != MediaAICRArtifact || digestBytes(layer) != aicrFixtureLayer {
		t.Fatalf("imported artifact differs from the producer push: %+v %v", descriptor, err)
	}
	again, err := ImportBundleLayout(t.Context(), filepath.Join(t.TempDir(), "again"), source, aicrFixtureManifest)
	if err != nil || again.RootDigest != result.RootDigest {
		t.Fatalf("import is unstable: %+v %v", again, err)
	}
	_, err = ImportBundleLayout(t.Context(), out, source, aicrFixtureManifest)
	assertRegistryCode(t, err, e.CodeImmutableViolation)
}

func TestImportBundleLayoutRefusesBeforeOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string) string
	}{
		{name: "manifest pin", mutate: func(t *testing.T, root string) string { return digestBytes([]byte("other")) }},
		{name: "layout marker", mutate: func(t *testing.T, root string) string {
			if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte(`{"imageLayoutVersion":"2.0.0"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return aicrFixtureManifest
		}},
		{name: "second manifest", mutate: func(t *testing.T, root string) string {
			raw, err := os.ReadFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(aicrFixtureManifest, "sha256:")))
			if err != nil {
				t.Fatal(err)
			}
			manifest := Descriptor{MediaType: MediaOCIManifest, Digest: aicrFixtureManifest, Size: int64(len(raw))}
			writeFixtureIndex(t, root, manifest, manifest)
			return aicrFixtureManifest
		}},
		{name: "index media type", mutate: func(t *testing.T, root string) string {
			writeFixtureIndex(t, root, Descriptor{MediaType: MediaOCIIndex, Digest: aicrFixtureManifest, Size: 930})
			return aicrFixtureManifest
		}},
		{name: "descriptor artifact type", mutate: func(t *testing.T, root string) string {
			writeFixtureIndex(t, root, Descriptor{MediaType: MediaOCIManifest, Digest: aicrFixtureManifest, Size: 930, ArtifactType: MediaBundle})
			return aicrFixtureManifest
		}},
		{name: "unlisted artifact type", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) { manifest["artifactType"] = MediaBundle })
		}},
		{name: "missing artifact type", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) { delete(manifest, "artifactType") })
		}},
		{name: "subject", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) { manifest["subject"] = manifest["config"] })
		}},
		{name: "config media type", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
				manifest["config"].(map[string]any)["mediaType"] = MediaImageConfig
			})
		}},
		{name: "non-empty config", mutate: func(t *testing.T, root string) string {
			raw := []byte(`{"a":1}`)
			digest := writeFixtureBlob(t, root, raw)
			removeFixtureBlob(t, root, digestBytes([]byte("{}")))
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
				config := manifest["config"].(map[string]any)
				config["digest"], config["size"] = digest, len(raw)
				delete(config, "data")
			})
		}},
		{name: "config data", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
				manifest["config"].(map[string]any)["data"] = "eyJhIjoxfQ=="
			})
		}},
		{name: "layer media type", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
				fixtureLayers(manifest)[0].(map[string]any)["mediaType"] = MediaBundleLayer
			})
		}},
		{name: "extra layer", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) {
				manifest["layers"] = append(fixtureLayers(manifest), fixtureLayers(manifest)[0])
			})
		}},
		{name: "no layer", mutate: func(t *testing.T, root string) string {
			return rewriteFixtureManifest(t, root, func(manifest map[string]any) { manifest["layers"] = []any{} })
		}},
		{name: "unreachable blob", mutate: func(t *testing.T, root string) string {
			writeFixtureBlob(t, root, []byte("extra"))
			return aicrFixtureManifest
		}},
		{name: "changed layer", mutate: func(t *testing.T, root string) string {
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(aicrFixtureLayer, "sha256:")), []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
			return aicrFixtureManifest
		}},
		{name: "archive link", mutate: func(t *testing.T, root string) string { return replaceFixtureLayer(t, root, symlinkArchive(t)) }},
		{name: "archive not gzip", mutate: func(t *testing.T, root string) string { return replaceFixtureLayer(t, root, []byte("plain bytes")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := aicrLayoutFixture(t)
			expected := test.mutate(t, source)
			out := filepath.Join(t.TempDir(), "imported")
			_, err := ImportBundleLayout(t.Context(), out, source, expected)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, statErr := os.Lstat(out); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("refused import left output: %v", statErr)
			}
		})
	}
}

func TestImportBundleLayoutCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := filepath.Join(t.TempDir(), "imported")
	_, err := ImportBundleLayout(ctx, out, aicrLayoutFixture(t), aicrFixtureManifest)
	assertRegistryCode(t, err, e.CodeTimeout)
	if _, statErr := os.Lstat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("canceled import left output: %v", statErr)
	}
}
