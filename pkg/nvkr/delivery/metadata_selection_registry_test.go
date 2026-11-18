// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullSelectedMetadataDoesNotRequireOtherControlsOrPayloads(t *testing.T) {
	full, pin := selectedMetadataFixture(t)
	inspection, err := inspectMetadata(t.Context(), full, true)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := newMetadataSelection([]string{"tools"})
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{}
	required := map[string]bool{}
	for _, object := range inspection.Root.Objects {
		manifest, err := flatManifest(t.Context(), full, object)
		if err != nil {
			t.Fatal(err)
		}
		if !selected.includes(object) {
			forbidden[object.DescriptorDigest], forbidden[manifest.Config.Digest] = true, true
		} else {
			required[object.DescriptorDigest], required[manifest.Config.Digest] = true, true
			if object.Evidence != nil {
				for _, layer := range manifest.Layers {
					required[layer.Digest] = true
				}
			}
		}
		if object.Output != nil || !selected.includes(object) {
			for _, layer := range manifest.Layers {
				forbidden[layer.Digest] = true
			}
		}
	}
	// The empty OCI config and other identical control bytes may be shared.
	for digest := range required {
		delete(forbidden, digest)
	}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var enabled atomic.Bool
	var forbiddenReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() && r.Method == http.MethodGet && forbidden[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]] {
			forbiddenReads.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	enabled.Store(true)
	out := filepath.Join(t.TempDir(), "selected")
	got, err := PullSelectedMetadata(t.Context(), reference, out, options, []string{"tools"})
	if err != nil || got.RootDigest != pin || !reflect.DeepEqual(got.Root, inspection.Root) {
		t.Fatal("selected pull lost original complete root or required missing unselected bytes", err)
	}
	if forbiddenReads.Load() != 0 {
		t.Fatal("selected pull attempted excluded control or payload reads", forbiddenReads.Load())
	}
	owner, err := OpenSelectedMetadata(t.Context(), out, pin, []string{"tools"})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMetadata(t.Context(), out); err == nil {
		t.Fatal("sparse selected metadata passed complete inspection")
	}
	ctx, budget, err := readbudget.WithLimit(t.Context(), 32)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PullSelectedMetadata(ctx, reference, filepath.Join(t.TempDir(), "exhausted"), options, []string{"tools"})
	if err == nil || budget.Check() == nil || budget.Used() != 32 {
		t.Fatal("selected registry acquisition escaped response allowance", err, budget.Used())
	}
}

func TestSelectedImageIndexMetadataRetainsExactChildren(t *testing.T) {
	full, result, _ := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	var output PublishedObject
	for _, object := range inspection.Root.Objects {
		if object.Output != nil {
			output = object
		}
	}
	_, payloads, err := imageIndexMetadata(t.Context(), full, output)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{}
	for _, payload := range payloads {
		forbidden[payload.Digest] = true
	}
	var enabled atomic.Bool
	var payloadReads atomic.Int64
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() && r.Method == http.MethodGet && forbidden[filepath.Base(r.URL.Path)] {
			payloadReads.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	enabled.Store(true)
	for _, source := range []string{"copy", "registry"} {
		t.Run(source, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "selected")
			if source == "copy" {
				_, err = CopySelectedMetadataWithExpectedRoot(t.Context(), full, out, result.RootDigest, []string{output.Name})
			} else {
				_, err = PullSelectedMetadata(t.Context(), reference, out, options, []string{output.Name})
			}
			if err != nil {
				t.Fatal(err)
			}
			owner, err := OpenSelectedMetadata(t.Context(), out, result.RootDigest, []string{output.Name})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := owner.Close(); err != nil {
					t.Error(err)
				}
			}()
			description, err := owner.Description(reference)
			if err != nil || len(description.Outputs) != 1 || !reflect.DeepEqual(description.Outputs[0].ImageIndex, output.Output.ImageIndex) {
				t.Fatal("selected index lost exact child membership", err)
			}
		})
	}
	if payloadReads.Load() != 0 {
		t.Fatal("selected index metadata fetched image payloads")
	}
}
