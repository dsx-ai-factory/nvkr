// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullMetadataActiveInterruption(t *testing.T) {
	full, rootDigest := releasePublicationFixture(t)
	index, manifest := fixtureManifest(t, full)
	// Every blocking boundary must propagate cancellation. Deadline and
	// permanent-failure classification each need one transport integration;
	// metadataFetchError's table owns the complete error-classification matrix.
	for _, test := range []struct{ phase, mode string }{
		{"index", "cancel"},
		{"manifest", "cancel"},
		{"blob-open", "cancel"},
		{"blob-read", "cancel"},
		{"blob-read", "deadline"},
		{"index", "failure"},
	} {
		phase, mode := test.phase, test.mode
		t.Run(phase+"/"+mode, func(t *testing.T) {
			target := "/manifests/" + rootDigest
			if phase == "manifest" {
				target = "/manifests/" + index.Manifests[0].Digest
			}
			if strings.HasPrefix(phase, "blob-") {
				target = "/blobs/" + manifest.Config.Digest
			}
			var armed, reached atomic.Bool
			var cancel context.CancelFunc
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !armed.Load() || r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, target) {
					handler.ServeHTTP(w, r)
					return
				}
				reached.Store(true)
				if mode == "failure" {
					http.Error(w, "ordinary fetch failure", http.StatusNotFound)
					return
				}
				if phase == "blob-read" {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{"))
					w.(http.Flusher).Flush()
				}
				if mode == "cancel" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/candidate/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			armed.Store(true)
			parent := t.TempDir()
			out := filepath.Join(parent, "metadata")
			_, err = PullMetadata(ctx, ref, out, options)
			if !reached.Load() {
				t.Fatal("request never reached active interruption point")
			}
			if mode == "failure" {
				assertRegistryCode(t, err, e.CodeFetchFailed)
			} else {
				assertRegistryCode(t, err, e.CodeTimeout)
				if !errors.Is(err, ctx.Err()) {
					t.Fatalf("lost interruption cause: %v", err)
				}
			}
			entries, readErr := os.ReadDir(parent)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed pull left output or staging files: %v / %v", entries, readErr)
			}
		})
	}
}

func TestPushMetadataActiveInterruption(t *testing.T) {
	full, rootDigest := releasePublicationFixture(t)
	for _, mode := range []string{"cancel", "deadline", "failure"} {
		t.Run(mode, func(t *testing.T) {
			var armed, reached atomic.Bool
			var cancel context.CancelFunc
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !armed.Load() || r.Method != http.MethodPut || r.URL.Path != "/v2/candidate/releases/manifests/"+rootDigest {
					handler.ServeHTTP(w, r)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				reached.Store(true)
				if mode == "failure" {
					http.Error(w, "ordinary registry failure", http.StatusServiceUnavailable)
					return
				}
				if mode == "cancel" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			prefix := strings.TrimPrefix(server.URL, "http://") + "/candidate"
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			if _, err := Push(t.Context(), full, prefix+"/releases-tools", options); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(t.TempDir(), "metadata")
			if _, err := ExportMetadata(t.Context(), full, metadata); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			armed.Store(true)
			ref, err := PushMetadata(ctx, metadata, prefix+"/releases", options)
			if !reached.Load() {
				t.Fatal("request never reached final metadata index upload")
			}
			if ref != "" {
				t.Fatalf("failed upload returned a publication reference: %s", ref)
			}
			if mode == "failure" {
				assertRegistryCode(t, err, e.CodeUnavailable)
			} else {
				assertRegistryCode(t, err, e.CodeTimeout)
				if !errors.Is(err, ctx.Err()) {
					t.Fatalf("lost interruption cause: %v", err)
				}
			}
		})
	}
}
