// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestNativeOVNProjectionUsesOnlyTheSelectedManifest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	reference := "localhost/nvke/ovn-kubernetes@" + digest
	for _, version := range []string{siteOVNVersion, upstreamSiteOVNVersion} {
		t.Run(version, func(t *testing.T) {
			raw, err := nativeOVNValues(version, reference)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal([]byte(raw), &values); err != nil {
				t.Fatal(err)
			}
			global := values["global"].(map[string]any)
			image := global["image"].(map[string]any)
			if version == siteOVNVersion {
				if global["useImageDigest"] != true || global["skipCallToK8s"] != true || global["imagePullSecretName"] != "" || image["repository"] != "localhost/nvke/ovn-kubernetes" || image["tag"] != "nvkr" || image["sha256"] != strings.TrimPrefix(digest, "sha256:") {
					t.Fatal("NVIDIA image override lost exact image or source settings")
				}
			} else if global["enableOvnKubeIdentity"] != false || values["skipCallToK8s"] != true || image["repository"] != "localhost/nvke/ovn-kubernetes@sha256" || image["tag"] != strings.TrimPrefix(digest, "sha256:") || image["pullPolicy"] != "IfNotPresent" {
				t.Fatal("upstream image override used the NVIDIA schema")
			}
			script, err := nativeImageImportScript("ovn.tar", reference)
			if err != nil || !strings.Contains(script, "--digests --base-name "+operatorQuote("localhost/nvke/ovn-kubernetes")) || !strings.Contains(script, "inspecti --quiet "+operatorQuote(reference)) {
				t.Fatal("native digest import lost repository/manifest binding")
			}
		})
	}
	for _, tc := range []struct{ version, reference string }{
		{siteOVNVersion, "localhost/nvke/ovn-kubernetes:latest"},
		{siteOVNVersion, "localhost/nvke/sibling@" + digest},
		{"latest", reference}, {siteOVNVersion, "sha256:" + strings.Repeat("a", 64)},
	} {
		if raw, err := nativeOVNValues(tc.version, tc.reference); err == nil || raw != "" {
			t.Fatal("unsupported image/schema produced values")
		}
	}
}

func TestNativeOVNArchiveRetainsTheExactObservedManifest(t *testing.T) {
	for _, tc := range []struct {
		name, arch  string
		wrongDigest bool
		valid       bool
	}{
		{"selected", "arm64", false, true}, {"sibling observation", "arm64", true, false}, {"wrong platform", "amd64", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := t.TempDir()
			image := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&tar.Header{Name: "fixture", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			image, err := mutate.AppendLayers(image, static.NewLayer(archive.Bytes(), types.OCIUncompressedLayer))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := image.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			cfg.OS, cfg.Architecture = "linux", tc.arch
			cfg.Config.Cmd = []string{"/bin/sh"}
			image, err = mutate.ConfigFile(image, cfg)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := image.Digest()
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(run, "packages", "ovn-image-arm64")
			index, err := layout.Write(root, empty.Index)
			if err != nil {
				t.Fatal(err)
			}
			if err := index.AppendImage(image); err != nil {
				t.Fatal(err)
			}
			observed := digest.String()
			if tc.wrongDigest {
				observed = "sha256:" + strings.Repeat("b", 64)
			}
			work := t.TempDir()
			files := filepath.Join(work, "files")
			if err := os.Mkdir(files, 0700); err != nil {
				t.Fatal(err)
			}
			reference := "localhost/nvke/ovn-kubernetes@" + observed
			got, err := prepareNativeImage(t.Context(), nativeImageSelection{role: "ovn", root: run, output: "ovn-image-arm64", reference: reference, observed: lifecycle.Artifact{Digest: observed}}, work, files)
			if (err == nil) != tc.valid {
				t.Fatalf("image projection: %v", err)
			}
			if !tc.valid {
				return
			}
			if got.Reference != reference || got.ManifestDigest != observed {
				t.Fatal("image alias changed selected subject")
			}
			inspected, err := delivery.InspectContainerImage(t.Context(), filepath.Join(work, "images", "ovn"), "linux/arm64")
			if err != nil || inspected.ManifestDigest != observed {
				t.Fatalf("projected manifest: %v", err)
			}
			if _, err := DigestFile(t.Context(), got.File); err != nil {
				t.Fatal(err)
			}
		})
	}
}
