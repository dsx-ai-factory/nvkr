// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestMetadataConcurrentByteBound(t *testing.T) {
	for _, count := range []int{4, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			writer := metadataBlobWriter{writer: layoutWriter{root: t.TempDir()}, limit: 16}
			raw := []byte("1234")
			d := Descriptor{Digest: digestBytes(raw), Size: 4}
			var wg sync.WaitGroup
			var accepted atomic.Int32
			for range count {
				wg.Go(func() {
					err := writer.put(raw, d, 4)
					if err == nil {
						accepted.Add(1)
					} else {
						assertRegistryCode(t, err, e.CodeVerifyFailed)
					}
				})
			}
			wg.Wait()
			if accepted.Load() != 4 || writer.total != 16 {
				t.Fatalf("bound exceeded: accepted=%d bytes=%d", accepted.Load(), writer.total)
			}
		})
	}
}

func TestPullMetadataConcurrentRequestsAndDeterministicLayout(t *testing.T) {
	full, _ := releasePublicationFixture(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var armed atomic.Bool
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tools/") {
			n := active.Add(1)
			defer active.Add(-1)
			for prior := peak.Load(); n > prior; prior = peak.Load() {
				if peak.CompareAndSwap(prior, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	var first map[string]string
	for range 2 {
		out := filepath.Join(t.TempDir(), "metadata")
		var updates []MetadataProgress
		_, err := PullMetadataWithOptions(t.Context(), ref, out, MetadataPullOptions{Registry: options, Progress: func(p MetadataProgress) { updates = append(updates, p) }})
		if err != nil {
			t.Fatal(err)
		}
		if len(updates) < 3 || updates[0].Done != 0 || updates[len(updates)-1].Done != updates[0].Total {
			t.Fatalf("incomplete progress: %+v", updates)
		}
		files := map[string]string{}
		err = filepath.WalkDir(out, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[strings.TrimPrefix(path, out)] = string(raw)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if first != nil && !reflect.DeepEqual(first, files) {
			t.Fatal("concurrent completion changed layout bytes")
		}
		first = files
	}
	if peak.Load() < 2 || peak.Load() > metadataConcurrency {
		t.Fatalf("request concurrency=%d", peak.Load())
	}
}

func TestPullMetadataObjectFailureCancelsPeers(t *testing.T) {
	full, _ := releasePublicationFixture(t)
	inspection, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Root.Objects) < 2 {
		t.Fatal("fixture needs concurrent objects")
	}
	failDigest := inspection.Root.Objects[0].DescriptorDigest
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var armed atomic.Bool
	peer := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tools/manifests/") {
			if strings.HasSuffix(r.URL.Path, failDigest) {
				select {
				case <-peer:
				case <-r.Context().Done():
					return
				}
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			once.Do(func() { close(peer) })
			<-r.Context().Done()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	parent := t.TempDir()
	_, err = PullMetadata(ctx, ref, filepath.Join(parent, "metadata"), options)
	assertRegistryCode(t, err, e.CodeFetchFailed)
	if ctx.Err() != nil {
		t.Fatal("peer cancellation waited for caller deadline")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed pull retained partial layout", err)
	}
}

func TestMetadataWorkerBoundAndJoin(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded success", true: "first failure cancels and joins"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			objects := make([]PublishedObject, 64)
			var active, peak, started, joined atomic.Int32
			ready := make(chan struct{})
			var once sync.Once
			err := fetchMetadataObjects(ctx, objects, func(ctx context.Context, _ PublishedObject) error {
				n := active.Add(1)
				defer active.Add(-1)
				defer joined.Add(1)
				for prior := peak.Load(); n > prior; prior = peak.Load() {
					if peak.CompareAndSwap(prior, n) {
						break
					}
				}
				order := started.Add(1)
				if order == metadataConcurrency {
					once.Do(func() { close(ready) })
				}
				select {
				case <-ready:
				case <-ctx.Done():
					return deliveryContext(ctx)
				}
				if fail {
					if order == 1 {
						return e.New(e.CodeVerifyFailed, "fixture member failure")
					}
					<-ctx.Done()
					return deliveryContext(ctx)
				}
				return nil
			})
			if fail {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
			if ctx.Err() != nil {
				t.Fatal("worker group did not finish before caller deadline")
			}
			if peak.Load() != metadataConcurrency || active.Load() != 0 || joined.Load() != started.Load() {
				t.Fatalf("peak=%d active=%d started=%d joined=%d", peak.Load(), active.Load(), started.Load(), joined.Load())
			}
			if !fail && joined.Load() != int32(len(objects)) {
				t.Fatal("success skipped objects")
			}
		})
	}
}
