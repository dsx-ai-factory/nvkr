// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

	"github.com/google/go-containerregistry/pkg/registry"
)

func TestMetadataCacheRechecksAndRepairs(t *testing.T) {
	full, digest := releasePublicationFixture(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	entry := filepath.Join(cache, strings.TrimPrefix(digest, "sha256:"))
	first := true
	var progressCount atomic.Int32
	pull := func() Inspection {
		t.Helper()
		got, err := PullMetadataCached(t.Context(), ref, filepath.Join(t.TempDir(), "out"), cache, MetadataPullOptions{Registry: options, Progress: func(MetadataProgress) {
			progressCount.Add(1)
			if first {
				if _, err := os.Stat(entry); !os.IsNotExist(err) {
					t.Error("cache entry became visible before closure validation", err)
				}
			}
		}})
		if err != nil || got.RootDigest != digest {
			t.Fatalf("cache pull: %v", err)
		}
		return got
	}
	requests.Store(0)
	got := pull()
	first = false
	coldRequests := requests.Load()
	if coldRequests == 0 {
		t.Fatal("cold pull did not reach registry")
	}
	for _, test := range []struct {
		name    string
		corrupt bool
	}{{"verified hit", false}, {"changed blob", true}, {"repaired hit", false}} {
		t.Run(test.name, func(t *testing.T) {
			if test.corrupt {
				path := filepath.Join(entry, "blobs", "sha256", strings.TrimPrefix(got.Root.Objects[0].DescriptorDigest, "sha256:"))
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw[len(raw)/2] ^= 1
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			requests.Store(0)
			progressCount.Store(0)
			pull()
			if progressCount.Load() < int32(len(got.Root.Objects)+1) {
				t.Fatal("cache verification did not report member progress")
			}
			if (requests.Load() > 0) != test.corrupt {
				t.Fatalf("registry requests=%d corrupt=%t", requests.Load(), test.corrupt)
			}
		})
	}
	// Simultaneous cold consumers publish once; their private copies are each
	// checked, and the second consumer cannot observe a partial cache stage.
	cache = t.TempDir()
	entry = filepath.Join(cache, strings.TrimPrefix(digest, "sha256:"))
	requests.Store(0)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { pull() })
	}
	wg.Wait()
	if requests.Load() != coldRequests {
		t.Fatalf("concurrent cold readers fetched more than once: %d != %d", requests.Load(), coldRequests)
	}
	if _, err := InspectMetadata(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".metadata-cache-") {
			t.Fatal("cache staging leaked")
		}
	}
}

func TestMetadataCacheDoesNotPublishFailedPull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "missing", http.StatusNotFound) }))
	defer server.Close()
	cache := t.TempDir()
	ref := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	_, err := PullMetadataCached(t.Context(), ref, filepath.Join(t.TempDir(), "out"), cache, MetadataPullOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}})
	if err == nil {
		t.Fatal("failed registry read succeeded")
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			t.Fatal("failed pull published a cache entry")
		}
	}
}

func TestMetadataCacheRetirementFailureCleansStageAndUnlocks(t *testing.T) {
	full, digest := releasePublicationFixture(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(handler)
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	entry := filepath.Join(cache, strings.TrimPrefix(digest, "sha256:"))
	if _, err := ExportMetadata(t.Context(), full, entry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "index.json"), []byte("corrupt cache index"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected, visited := false, false
	request := MetadataPullOptions{Registry: options, Progress: func(MetadataProgress) {
		if injected {
			return
		}
		entries, err := os.ReadDir(cache)
		if err != nil {
			t.Error(err)
			return
		}
		for _, candidate := range entries {
			if !strings.HasPrefix(candidate.Name(), ".metadata-cache-") {
				continue
			}
			// A nonempty destination forces Rename to fail without relying on uid,
			// filesystem permissions, or removing the already checked replacement.
			rejected := filepath.Join(cache, candidate.Name(), "rejected")
			if err := os.Mkdir(rejected, 0o700); err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(rejected, "blocker"), []byte("fixture"), 0o600); err != nil {
				t.Error(err)
				return
			}
			injected = true
		}
	}}
	visit := func(context.Context, string, Inspection) error { visited = true; return nil }
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	err = VisitCachedMetadata(ctx, ref, cache, request, visit)
	assertRegistryCode(t, err, e.CodeImmutableViolation)
	if !strings.Contains(err.Error(), "retiring invalid metadata cache") || !injected || visited {
		t.Fatalf("did not fail during retirement: injected=%t visited=%t err=%v", injected, visited, err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range entries {
		if strings.HasPrefix(candidate.Name(), ".metadata-cache-") {
			t.Fatal("failed retirement leaked staging")
		}
	}
	// A subsequent visit must acquire the released lock, refetch the still-invalid
	// entry and reach the visitor only after successful atomic replacement.
	if err := VisitCachedMetadata(ctx, ref, cache, MetadataPullOptions{Registry: options}, visit); err != nil {
		t.Fatal(err)
	}
	if !visited {
		t.Fatal("repaired cache was not visited")
	}
}
