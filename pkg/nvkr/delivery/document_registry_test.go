// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestDocumentRegistryRoundTrip(t *testing.T) {
	var tagWrites atomic.Int32
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") && !strings.Contains(r.URL.Path, "/manifests/sha256:") {
			tagWrites.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repo := strings.TrimPrefix(server.URL, "http://") + "/documents"
	media := "application/vnd.nvkr.test-document.v1+json"
	raw := []byte(`{"fixture":"exact bytes"}`)
	ref, err := PushDocumentArtifact(t.Context(), repo, media, raw, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PushDocumentArtifact(t.Context(), repo, media, raw, options)
	if err != nil || second != ref {
		t.Fatal("document transport is not deterministic", err)
	}
	if tagWrites.Load() != 0 {
		t.Fatal("document publication wrote a tag")
	}
	for _, mode := range []string{"exact", "bound", "media", "unpinned", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			pin, kind, limit := ref, media, int64(len(raw))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeVerifyFailed
			switch mode {
			case "bound":
				limit--
			case "media":
				kind = "application/vnd.nvkr.other+json"
			case "unpinned":
				pin = repo + ":latest"
				code = e.CodeUnpinnedSource
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			got, err := PullDocumentArtifact(ctx, pin, kind, limit, options)
			if mode != "exact" {
				assertRegistryCode(t, err, code)
				if got != nil {
					t.Fatal("refused document returned bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, raw) {
				t.Fatal("document bytes changed")
			}
		})
	}
}
