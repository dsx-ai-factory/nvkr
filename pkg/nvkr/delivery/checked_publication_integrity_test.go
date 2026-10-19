// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCheckedPublicationRejectsSelfPinnedEvidenceConfig(t *testing.T) {
	root, _ := releasePublicationFixture(t)
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var evidence PublishedObject
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil {
			evidence = object
			break
		}
	}
	manifest, err := flatManifest(t.Context(), root, evidence)
	if err != nil {
		t.Fatal(err)
	}
	config := objectConfig{SchemaVersion: ObjectConfigSchema, Name: "different-evidence", Kind: evidence.Kind,
		ArtifactType: evidence.ArtifactType, SemanticDigest: evidence.SemanticDigest,
		BlobDigest: evidence.BlobDigest, Size: manifest.Layers[0].Size}
	oldConfig := manifest.Config.Digest
	writer := layoutWriter{root: root}
	manifest.Config, err = writer.addJSONBlob(config, MediaDeliveryObjectConfig)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixtureBlobPath(root, oldConfig)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixtureBlobPath(root, evidence.DescriptorDigest)); err != nil {
		t.Fatal(err)
	}
	pin := rewriteDiskPublicationRoot(t, root, inspection, func(publication *PublicationRoot) {
		for i := range publication.Objects {
			if publication.Objects[i].Name == evidence.Name {
				publication.Objects[i].DescriptorDigest = changed.Digest
				publication.Objects[i].Size = changed.Size
			}
		}
	})
	_, err = Inspect(t.Context(), root)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	// Serve the intentionally inconsistent, self-pinned closure directly: Push
	// correctly refuses it before network writes, so it cannot construct this peer.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		digest := filepath.Base(r.URL.Path)
		if !isDigest(digest) {
			http.NotFound(w, r)
			return
		}
		path := fixtureBlobPath(root, digest)
		if digest == pin {
			path = filepath.Join(root, "index.json")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		media := "application/octet-stream"
		if strings.Contains(r.URL.Path, "/manifests/") {
			media = MediaOCIManifest
			if digest == pin {
				media = MediaOCIIndex
			}
		}
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("Docker-Content-Digest", digest)
		if r.Method != http.MethodHead {
			_, _ = w.Write(raw)
		}
	}))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@" + pin
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	if _, err := PullMetadata(t.Context(), reference, filepath.Join(t.TempDir(), "controls"), options); err != nil {
		t.Fatalf("fixture did not isolate evidence binding: %v", err)
	}
	workspace := t.TempDir()
	checked, err := PullCheckedPublication(t.Context(), reference, workspace, options)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if checked != nil {
		t.Fatal("inconsistent evidence config issued owner")
	}
	assertEmptyPullDirectory(t, workspace)
}

func TestCheckedPublicationAdmitsBeforeWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ref := "registry.example.test/product/releases@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name      string
		ctx       context.Context
		reference string
		options   RegistryOptions
		code      e.Code
	}{
		{"nil context", nil, ref, RegistryOptions{}, e.CodeUsage},
		{"canceled", canceled, ref, RegistryOptions{}, e.CodeTimeout},
		{"tag", t.Context(), "registry.example.test/product/releases:tag", RegistryOptions{}, e.CodeUnverifiedRef},
		{"partial credential", t.Context(), ref, RegistryOptions{Username: "reader"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "untouched")
			owner, err := PullCheckedPublication(test.ctx, test.reference, workspace, test.options)
			assertRegistryCode(t, err, test.code)
			if owner != nil {
				t.Fatal("invalid request issued owner")
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatal("invalid request touched workspace", err)
			}
		})
	}
}
