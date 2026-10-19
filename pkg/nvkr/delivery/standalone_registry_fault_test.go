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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestStandaloneRegistryPayloadFailures(t *testing.T) {
	for _, kind := range []string{"disk", "document"} {
		for _, mode := range []string{"cancel", "truncate", "corrupt", "destination-appears"} {
			if kind == "document" && mode == "destination-appears" {
				continue
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				parent := t.TempDir()
				out := filepath.Join(parent, "download")
				var armed atomic.Bool
				var served atomic.Int32
				joined := make(chan struct{}, 4)
				raw := bytes.Repeat([]byte("document bytes"), 32000)
				var owner *DiskArtifact
				if kind == "disk" {
					root, payload, manifest := diskFixture(t, "linux/arm64")
					raw = payload
					var err error
					owner, err = OpenDiskArtifact(ctx, root, manifest, "linux/arm64", int64(len(payload)))
					if err != nil {
						t.Fatal(err)
					}
					defer owner.Close()
				}
				digest := digestBytes(raw)
				handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				var appeared os.FileInfo
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !armed.Load() || r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/blobs/"+digest) {
						handler.ServeHTTP(w, r)
						return
					}
					served.Add(1)
					defer func() { joined <- struct{}{} }()
					w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
					w.Header().Set("Content-Type", "application/octet-stream")
					switch mode {
					case "cancel":
						_, _ = w.Write(raw[:4096])
						w.(http.Flusher).Flush()
						cancel()
						select {
						case <-r.Context().Done():
						case <-time.After(5 * time.Second):
							t.Error("canceled request did not join")
						}
					case "truncate":
						_, _ = w.Write(raw[:4096])
					case "corrupt":
						changed := bytes.Clone(raw)
						changed[len(changed)-1] ^= 1
						_, _ = w.Write(changed)
					case "destination-appears":
						if err := os.Mkdir(out, 0700); err != nil {
							t.Error(err)
						}
						var err error
						appeared, err = os.Stat(out)
						if err != nil {
							t.Error(err)
						}
						_, _ = w.Write(raw)
					}
				}))
				defer server.Close()
				options := RegistryOptions{PlainHTTP: true, Anonymous: true}
				repo := strings.TrimPrefix(server.URL, "http://") + "/fault"
				media := "application/vnd.nvkr.test-document.v1+json"
				var ref string
				var err error
				if kind == "disk" {
					ref, err = PushDiskArtifact(ctx, owner, repo, t.TempDir(), options)
				} else {
					ref, err = PushDocumentArtifact(ctx, repo, media, raw, options)
				}
				if err != nil {
					t.Fatal(err)
				}
				armed.Store(true)
				var got []byte
				if kind == "disk" {
					err = PullDiskArtifact(ctx, ref, out, "linux/arm64", int64(len(raw)), options)
				} else {
					got, err = PullDocumentArtifact(ctx, ref, media, int64(len(raw)), options)
				}
				code := e.CodeUnavailable
				if mode == "cancel" {
					code = e.CodeTimeout
				}
				if mode == "destination-appears" {
					code = e.CodeImmutableViolation
				}
				assertRegistryCode(t, err, code)
				if got != nil {
					t.Fatal("failed document returned payload bytes")
				}
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Fatal("payload handler did not finish")
				}
				if served.Load() != 1 {
					t.Fatalf("payload requests=%d", served.Load())
				}
				entries, readErr := os.ReadDir(parent)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if mode == "destination-appears" {
					after, statErr := os.Stat(out)
					if statErr != nil || appeared == nil || !os.SameFile(appeared, after) || len(entries) != 1 {
						t.Fatal("competing destination changed or staging leaked", statErr)
					}
					children, readErr := os.ReadDir(out)
					if readErr != nil || len(children) != 0 {
						t.Fatal("competing empty directory populated", readErr)
					}
				} else if len(entries) != 0 {
					t.Fatalf("failed download retained staging/output: %v", entries)
				}
			})
		}
	}
}
