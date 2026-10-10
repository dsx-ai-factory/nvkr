// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestSeparateRepositoryPublicationAndConsumption(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	outputDigest := ""
	for _, object := range inspection.Root.Objects {
		if object.Output != nil {
			outputDigest = object.DescriptorDigest
		}
	}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	mode := ""
	writes := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
			writes[r.URL.Path] = true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/product/tools/manifests/"+outputDigest {
			switch mode {
			case "missing":
				w.WriteHeader(http.StatusNotFound)
				return
			case "denied":
				w.WriteHeader(http.StatusForbidden)
				return
			case "substituted":
				w.Header().Set("Content-Type", delivery.MediaOCIManifest)
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	target := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	options := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), layout, target, options)
	if err != nil {
		t.Fatal(err)
	}
	if reference != target+"@"+digest {
		t.Fatal("release identity changed", reference)
	}
	mu.Lock()
	if !writes["/v2/product/tools/manifests/"+outputDigest] || writes["/v2/product/releases/manifests/"+outputDigest] {
		t.Error("deliverable was not isolated in its own repository", writes)
	}
	if !writes["/v2/product/releases/manifests/"+digest] {
		t.Error("missing release entry point")
	}
	mu.Unlock()
	for _, tc := range []struct {
		mode string
		code e.Code
	}{{"", ""}, {"missing", e.CodeUnavailable}, {"denied", e.CodeUnavailable}, {"substituted", e.CodeUnavailable}} {
		t.Run(tc.mode, func(t *testing.T) {
			mu.Lock()
			mode = tc.mode
			mu.Unlock()
			out := filepath.Join(t.TempDir(), "pulled")
			actual, err := delivery.Pull(t.Context(), reference, out, options)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("failed pull exposed partial closure", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			artifacts, _, err := inspectTestPublication(t, t.Context(), out, actual.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			if len(artifacts) != 1 || artifacts[0].Digest != outputDigest {
				t.Fatal("wrong reconstructed deliverable", artifacts)
			}
		})
	}
}

func TestRepositoryCollisionFailsBeforePublishing(t *testing.T) {
	layout, _ := testPublicationLayout(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	_, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/tools", delivery.RegistryOptions{Anonymous: true, PlainHTTP: true})
	assertCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("repository collision reached registry")
	}
}

func TestColocatedEvidenceDiscoveryAndMissingEvidence(t *testing.T) {
	layout, _ := testPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	var evidence, output delivery.PublishedObject
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil {
			evidence = object
		}
		if object.Output != nil {
			output = object
		}
	}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	mode := ""
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/v2/product/tools/manifests/"+evidence.DescriptorDigest {
			if mode == "missing" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if mode == "substituted" {
				w.Header().Set("Content-Type", delivery.MediaOCIManifest)
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	options := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), layout, host+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := delivery.EvidenceReferences(reference, inspection)
	if err != nil {
		t.Fatal(err)
	}
	if refs[evidence.Name] != host+"/product/tools@"+evidence.DescriptorDigest {
		t.Fatal("wrong evidence repository", refs)
	}
	for _, tc := range []struct {
		repository, digest string
		status             int
	}{
		{"tools", evidence.DescriptorDigest, 200}, {"releases", evidence.DescriptorDigest, 404},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v2/product/"+tc.repository+"/manifests/"+tc.digest, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("%s evidence status %d", tc.repository, response.StatusCode)
		}
	}
	subject, err := name.NewDigest(host+"/product/tools@"+output.DescriptorDigest, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	referrers, err := remote.Referrers(subject, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous))
	if err != nil {
		t.Fatal(err)
	}
	index, err := referrers.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest.String() != evidence.DescriptorDigest {
		t.Fatalf("evidence not discoverable: %+v", index)
	}
	for _, failure := range []string{"missing", "substituted"} {
		t.Run(failure, func(t *testing.T) {
			mu.Lock()
			mode = failure
			mu.Unlock()
			out := filepath.Join(t.TempDir(), "closure")
			_, err := delivery.Pull(t.Context(), reference, out, options)
			assertCode(t, err, e.CodeUnavailable)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed evidence fetch exposed output")
			}
		})
	}
}
