// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestImageIndexMetadataTransportPreservesChildrenWithoutPayloads(t *testing.T) {
	full, result, _ := registryImageIndexFixture(t)
	inspected, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	var object PublishedObject
	for _, o := range inspected.Root.Objects {
		if o.Output != nil {
			object = o
		}
	}
	controls, payloads, err := imageIndexMetadata(t.Context(), full, object)
	if err != nil {
		t.Fatal(err)
	}
	payloadSet := map[string]bool{}
	for _, d := range payloads {
		payloadSet[d.Digest] = true
	}
	var reads atomic.Int64
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && payloadSet[filepath.Base(r.URL.Path)] {
			reads.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	prefix := strings.TrimPrefix(server.URL, "http://") + "/candidate"
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), full, prefix+"/releases-cwe", options)
	if err != nil {
		t.Fatal(err)
	}
	reads.Store(0)
	metadata := filepath.Join(t.TempDir(), "metadata")
	got, err := PullMetadata(t.Context(), ref, metadata, options)
	if err != nil {
		t.Fatal(err)
	}
	if got.RootDigest != result.RootDigest || reads.Load() != 0 {
		t.Fatal("metadata changed root or fetched payload")
	}
	exported := filepath.Join(t.TempDir(), "exported")
	ex, err := ExportMetadata(t.Context(), full, exported)
	if err != nil || ex.RootDigest != result.RootDigest {
		t.Fatalf("export: %v", err)
	}
	expected, err := MetadataOutputs(inspected.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{full, metadata, exported} {
		description, err := InspectMetadataDescription(t.Context(), path, ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(description.Outputs) != 1 || !reflect.DeepEqual(description.Outputs[0].ImageIndex, object.Output.ImageIndex) {
			t.Fatal("metadata description lost index membership")
		}
		view, err := InspectMetadata(t.Context(), exported)
		if err != nil {
			t.Fatal(err)
		}
		outputs, err := MetadataOutputs(view.Root)
		if err != nil || !reflect.DeepEqual(outputs, expected) {
			t.Fatalf("metadata lost child membership: %+v %v", outputs, err)
		}
	}
	for digest := range payloadSet {
		if _, err := os.Stat(filepath.Join(metadata, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); !os.IsNotExist(err) {
			t.Fatalf("metadata contains payload: %v", err)
		}
	}
	if _, err := Inspect(t.Context(), metadata); err == nil {
		t.Fatal("metadata issued full payload validation")
	}
	release, err := PushMetadata(t.Context(), metadata, prefix+"/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 0 {
		t.Fatal("metadata push fetched payload")
	}
	recovered, err := Pull(t.Context(), release, filepath.Join(t.TempDir(), "consumer"), options)
	if err != nil || recovered.RootDigest != result.RootDigest {
		t.Fatalf("full consumer: %v", err)
	}
	if reads.Load() == 0 {
		t.Fatal("full consumer did not recover layers")
	}
	for _, d := range controls[1:] {
		t.Run(d.Digest, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "missing")
			if _, err := ExportMetadata(t.Context(), full, destination); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(destination, "blobs", "sha256", strings.TrimPrefix(d.Digest, "sha256:"))); err != nil {
				t.Fatal(err)
			}
			_, err := InspectMetadata(t.Context(), destination)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	changed := object
	binding := *object.Output
	binding.ImageIndex = object.Output.ImageIndex.Clone()
	binding.ImageIndex.Children[0].ManifestDigest = digestBytes([]byte("substituted"))
	changed.Output = &binding
	_, _, err = imageIndexMetadata(t.Context(), metadata, changed)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}
