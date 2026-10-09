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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullMetadataClassifiesFailuresAfterRootAcquisition(t *testing.T) {
	root, rootDigest := releasePublicationFixture(t)
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	index, _, err := readIndex(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var memberManifest, memberConfig, evidenceBlob string
	var memberConfigBytes []byte
	payloads := map[string]bool{}
	for _, object := range inspection.Root.Objects {
		manifest, err := flatManifest(t.Context(), root, object)
		if err != nil {
			t.Fatal(err)
		}
		if object.Output != nil {
			memberManifest = "/manifests/" + object.DescriptorDigest
			memberConfig = "/blobs/" + manifest.Config.Digest
			memberConfigBytes, err = readBlob(t.Context(), root, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			for _, layer := range manifest.Layers {
				payloads[layer.Digest] = true
			}
		}
		if object.Evidence != nil {
			evidenceBlob = "/blobs/" + manifest.Layers[0].Digest
		}
	}
	if memberManifest == "" || memberConfig == "" || evidenceBlob == "" || len(memberConfigBytes) < 2 || len(payloads) == 0 {
		t.Fatal("release fixture lacks output metadata, evidence or payloads")
	}
	for _, test := range []struct {
		name, suffix, failure string
		code                  e.Code
		retryable             bool
	}{
		{name: "successful control"},
		{"root manifest unavailable", "/manifests/" + index.Manifests[0].Digest, "unavailable", e.CodeUnavailable, true},
		{"member manifest unavailable", memberManifest, "unavailable", e.CodeUnavailable, true},
		{"root config unavailable", "/blobs/" + inspection.RootConfig.Digest, "unavailable", e.CodeUnavailable, true},
		{"member config unavailable", memberConfig, "unavailable", e.CodeUnavailable, true},
		{"evidence blob unavailable", evidenceBlob, "unavailable", e.CodeUnavailable, true},
		{"truncated blob stream", memberConfig, "truncated", e.CodeUnavailable, true},
		{"corrupt successful blob", memberConfig, "corrupt", e.CodeFetchFailed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var enabled atomic.Bool
			var faultRequests, payloadRequests atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if enabled.Load() && r.Method == http.MethodGet {
					if _, digest, ok := strings.Cut(r.URL.Path, "/blobs/"); ok && payloads[digest] {
						payloadRequests.Add(1)
						http.Error(w, "artifact payload download forbidden", http.StatusForbidden)
						return
					}
					if test.failure != "" && strings.HasSuffix(r.URL.Path, test.suffix) {
						faultRequests.Add(1)
						switch test.failure {
						case "unavailable":
							http.Error(w, "metadata temporarily unavailable", http.StatusServiceUnavailable)
						case "truncated":
							// A declared full response that ends early is a transport
							// outage, unlike a complete response with corrupt bytes.
							w.Header().Set("Content-Length", strconv.Itoa(len(memberConfigBytes)))
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write(memberConfigBytes[:len(memberConfigBytes)/2])
						case "corrupt":
							raw := append([]byte(nil), memberConfigBytes...)
							raw[0] ^= 1
							w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write(raw)
						}
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			enabled.Store(true)
			workspace := t.TempDir()
			out := filepath.Join(workspace, "metadata")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			got, err := PullMetadata(ctx, reference, out, options)
			if payloadRequests.Load() != 0 {
				t.Fatalf("metadata acquisition requested %d artifact payload layers", payloadRequests.Load())
			}
			if test.failure == "" {
				if err != nil || got.RootDigest != rootDigest {
					t.Fatalf("successful metadata control: root=%q, error=%v", got.RootDigest, err)
				}
				for digest := range payloads {
					if _, err := os.Stat(fixtureBlobPath(out, digest)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("metadata layout retained artifact payload %s: %v", digest, err)
					}
				}
				return
			}
			assertRegistryCode(t, err, test.code)
			if e.Retryable(err) != test.retryable {
				t.Fatalf("retryable=%t, want %t: %v", e.Retryable(err), test.retryable, err)
			}
			if faultRequests.Load() == 0 {
				t.Fatal("metadata failure did not reach the selected manifest or blob")
			}
			if test.failure == "truncated" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated blob lost its stream error: %v", err)
			}
			if test.failure == "corrupt" && !strings.Contains(err.Error(), "checksum") {
				t.Fatalf("corrupt successful blob did not reach upstream checksum verification: %v", err)
			}
			if !reflect.DeepEqual(got, Inspection{}) {
				t.Fatalf("failed acquisition returned partial metadata: %+v", got)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed acquisition exposed an output or retained staging: %v, %v", entries, err)
			}
		})
	}
}
