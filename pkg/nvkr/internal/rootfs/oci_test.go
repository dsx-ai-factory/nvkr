// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
)

func runtimeLayoutFixture(t *testing.T, mutateConfig func(map[string]any), mutateManifest func(*ociclosure.ManifestDocument)) (string, string, string) {
	t.Helper()
	layout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(layout, path), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	blob := func(raw []byte, media string) ociclosure.Descriptor {
		t.Helper()
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		write(runtimeBlobPath(digest), raw)
		return ociclosure.Descriptor{Digest: digest, Size: int64(len(raw)), MediaType: media}
	}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	held := layerFixture(t, []tar.Header{rootHeader()}, nil)
	layerRaw, err := held.ReadAllContext(t.Context(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	layer := blob(layerRaw, ociLayer)
	config := map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{layer.Digest}},
		"config": map[string]any{"Cmd": []string{"/sbin/init"}}}
	if mutateConfig != nil {
		mutateConfig(config)
	}
	manifest := ociclosure.ManifestDocument{SchemaVersion: 2, MediaType: ociManifest, Config: blob(encode(config), ociConfig), Layers: []ociclosure.Descriptor{layer}}
	if mutateManifest != nil {
		mutateManifest(&manifest)
	}
	root := blob(encode(manifest), ociManifest)
	write("index.json", encode(ociclosure.IndexDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []ociclosure.Descriptor{root}}))
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	return layout, root.Digest, layer.Digest
}

func TestCaptureRuntimeLayerHoldsExactBytes(t *testing.T) {
	layout, digest, layerDigest := runtimeLayoutFixture(t, nil, nil)
	layer, err := CaptureLayer(t.Context(), layout, digest, "linux/amd64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	if layer.Digest() != layerDigest {
		t.Fatal("captured a different layer")
	}
	if err := os.WriteFile(filepath.Join(layout, runtimeBlobPath(layerDigest)), []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := layer.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatalf("held layer followed mutable producer path: %v", err)
	}
	tree, err := Stage(t.Context(), layer, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if second, err := CaptureLayer(t.Context(), layout, digest, "linux/amd64", t.TempDir()); err == nil {
		_ = second.Close()
		t.Fatal("recapture accepted substituted producer bytes")
	}
}

func TestCaptureRuntimeLayerRejectsOtherOCIForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   func(map[string]any)
		manifest func(*ociclosure.ManifestDocument)
	}{
		{"foreign architecture", func(c map[string]any) { c["architecture"] = "arm64" }, nil},
		{"foreign OS", func(c map[string]any) { c["os"] = "windows" }, nil},
		{"different diff ID", func(c map[string]any) {
			c["rootfs"] = map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + strings.Repeat("1", 64)}}
		}, nil},
		{"process environment", func(c map[string]any) { c["config"] = map[string]any{"Env": []string{"LD_PRELOAD=/host/library"}} }, nil},
		{"case alias", func(c map[string]any) { c["Architecture"] = "arm64" }, nil},
		{"multiple layers", nil, func(m *ociclosure.ManifestDocument) { m.Layers = append(m.Layers, m.Layers[0]) }},
		{"compressed layer", nil, func(m *ociclosure.ManifestDocument) { m.Layers[0].MediaType += "+gzip" }},
		{"wrong config media", nil, func(m *ociclosure.ManifestDocument) {
			m.Config.MediaType = "application/vnd.docker.container.image.v1+json"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, digest, _ := runtimeLayoutFixture(t, tc.config, tc.manifest)
			layer, err := CaptureLayer(t.Context(), layout, digest, "linux/amd64", t.TempDir())
			if layer != nil {
				_ = layer.Close()
				t.Fatal("rejected OCI form returned held bytes")
			}
			requireStageCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestCaptureRuntimeLayerRejectsUnreferencedBlob(t *testing.T) {
	layout, digest, _ := runtimeLayoutFixture(t, nil, nil)
	raw := []byte("unreferenced")
	name := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if err := os.WriteFile(filepath.Join(layout, runtimeBlobPath(name)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := CaptureLayer(t.Context(), layout, digest, "linux/amd64", t.TempDir())
	requireStageCode(t, err, e.CodeVerifyFailed)
}
