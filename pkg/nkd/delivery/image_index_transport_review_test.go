// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestImageIndexRejectsChildMediaMismatch(t *testing.T) {
	for _, route := range []string{"assemble", "open"} {
		t.Run(route, func(t *testing.T) {
			inputs := imageIndexInputs(t)
			if route == "assemble" {
				rewriteImageChild(t, &inputs[0], func(m *ociManifest, _ map[string]any) {
					m.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
					m.Config.MediaType = "application/vnd.docker.container.image.v1+json"
				})
				got, err := AssembleImageIndex(t.Context(), imageIndexPlatforms, inputs)
				if got != nil {
					defer got.Close()
					t.Error("mismatched child admitted")
				}
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			root := writeIndexFixture(t, assembledImageIndex(t, inputs))
			var index runnableIndex
			if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "index.json")), &index); err != nil {
				t.Fatal(err)
			}
			old := index.Manifests[0].Digest
			var manifest ociManifest
			if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
			manifest.Config.MediaType = "application/vnd.docker.container.image.v1+json"
			descriptor, err := (layoutWriter{root: root}).addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			index.Manifests[0].Descriptor = descriptor
			if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))); err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			writeImageTestFile(t, filepath.Join(root, "index.json"), raw)
			got, err := OpenImageIndex(t.Context(), root, digestBytes(raw), imageIndexPlatforms)
			if got != nil {
				defer got.Close()
				t.Error("mismatched child reopened")
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestRegistryImageIndexPullBlobFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"interrupted", e.CodeUnavailable}, {"corrupt", e.CodeVerifyFailed}, {"oversized", e.CodeVerifyFailed}, {"canceled", e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publication, _, _ := registryImageIndexFixture(t)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var broken atomic.Bool
			var hits atomic.Int32
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if broken.Load() && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/product/cwe/blobs/") {
					hits.Add(1)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					if rec.Code != http.StatusOK {
						t.Errorf("fixture blob status %d", rec.Code)
						w.WriteHeader(rec.Code)
						return
					}
					body := append([]byte(nil), rec.Body.Bytes()...)
					switch tc.name {
					case "interrupted":
						w.Header().Set("Content-Length", strconv.Itoa(len(body)))
						body = body[:3]
					case "corrupt":
						body[0] ^= 1
					case "oversized":
						body = append(body, 0)
					case "canceled":
						cancel()
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(t.Context(), publication, strings.TrimPrefix(server.URL, "http://")+"/product/release", options)
			if err != nil {
				t.Fatal(err)
			}
			broken.Store(true)
			out := filepath.Join(t.TempDir(), "absent")
			_, err = Pull(ctx, ref, out, options)
			assertRegistryCode(t, err, tc.code)
			if tc.name == "interrupted" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("lost interrupted read cause: %v", err)
			}
			if hits.Load() == 0 {
				t.Fatal("aggregate blob route not exercised")
			}
			entries, readErr := os.ReadDir(filepath.Dir(out))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("partial pull exposed output: %v/%v", entries, readErr)
			}
		})
	}
}

// Keep the outer context live so classifyContext cannot mask the inner cause.
func TestRegistryBlobInnerTimeoutClassification(t *testing.T) {
	for _, cause := range []error{fmt.Errorf("reader deadline: %w", context.DeadlineExceeded), &net.DNSError{Err: "reader timeout", IsTimeout: true}} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := t.Context()
			repository, err := name.NewRepository("registry.example.test/product", name.StrictValidation)
			if err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			transport := imageErrorTransport(func(r *http.Request) (*http.Response, error) {
				body := io.NopCloser(strings.NewReader("{}"))
				if strings.Contains(r.URL.Path, "/blobs/") {
					body = io.NopCloser(&imageErrorReader{cause: cause, reads: &reads})
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r, ContentLength: -1}, nil
			})
			closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
			defer closure.close()
			options := append(registryOptions(ctx, RegistryOptions{Anonymous: true}), remote.WithTransport(transport))
			err = captureRegistryBlob(ctx, closure, repository, Descriptor{Digest: digestBytes([]byte("payload")), Size: 7}, 8<<20, options)
			assertRegistryCode(t, err, e.CodeTimeout)
			if ctx.Err() != nil || reads.Load() == 0 || !errors.Is(err, cause) {
				t.Fatalf("inner timeout not isolated or preserved: %v, outer=%v, reads=%d", err, ctx.Err(), reads.Load())
			}
			if len(closure.blobs) != 0 || closure.bytes != 0 {
				t.Fatal("failed capture retained partial blob")
			}
		})
	}
}

type imageErrorTransport func(*http.Request) (*http.Response, error)

func (f imageErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type imageErrorReader struct {
	cause error
	reads *atomic.Int32
}

func (r *imageErrorReader) Read([]byte) (int, error) { r.reads.Add(1); return 0, r.cause }

// Storage traversal is deliberately broader than runnable aggregate admission.
func TestOpenPublicationRejectsDockerChildAfterStorageSelection(t *testing.T) {
	root, _, _ := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	publication := inspection.Root
	writer := layoutWriter{root: root}
	var index ociIndex
	if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "index.json")), &index); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]Descriptor{}
	var outputs []Output
	for i := range publication.Objects {
		object := &publication.Objects[i]
		if object.Output == nil {
			continue
		}
		var aggregate runnableIndex
		if err := json.Unmarshal(mustReadImageFile(t, fixtureBlobPath(root, object.DescriptorDigest)), &aggregate); err != nil {
			t.Fatal(err)
		}
		aggregate.Manifests[0].MediaType = "application/vnd.docker.distribution.manifest.v2+json"
		descriptor, err := writer.addJSONBlob(aggregate, MediaOCIIndex)
		if err != nil {
			t.Fatal(err)
		}
		replacements[object.DescriptorDigest] = descriptor
		object.DescriptorDigest, object.Size = descriptor.Digest, descriptor.Size
		object.BlobDigest, object.SemanticDigest = descriptor.Digest, descriptor.Digest
		outputs = append(outputs, Output{Name: object.Name, Kind: object.Output.Format, Digest: object.BlobDigest, BuildDigest: object.Output.BuildDigest, Components: object.Output.Components, ImageIndex: object.Output.ImageIndex})
	}
	publication.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := publication.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	config, err := writer.addBlobBytes(raw, MediaPublicationRootConfig)
	if err != nil {
		t.Fatal(err)
	}
	for i := range index.Manifests {
		old := index.Manifests[i]
		if old.ArtifactType == MediaPublicationRoot {
			var manifest ociManifest
			if err := json.Unmarshal(mustReadImageFile(t, fixtureBlobPath(root, old.Digest)), &manifest); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(fixtureBlobPath(root, manifest.Config.Digest)); err != nil {
				t.Fatal(err)
			}
			manifest.Config = config
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			descriptor.ArtifactType = MediaPublicationRoot
			replacements[old.Digest] = descriptor
		}
		if replacement, ok := replacements[old.Digest]; ok {
			index.Manifests[i] = replacement
		}
	}
	for old := range replacements {
		if err := os.Remove(fixtureBlobPath(root, old)); err != nil {
			t.Fatal(err)
		}
	}
	pin, err := writer.writeIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	disks, err := publicationDiskPayloads(t.Context(), root, pin)
	if err != nil {
		t.Fatalf("storage selector rejected native traversal: %v", err)
	}
	for _, disk := range disks {
		_ = disk.Close()
	}
	held, err := OpenPublication(t.Context(), root, pin)
	if held != nil {
		_ = held.Close()
		t.Fatal("Docker child admitted as OCI aggregate")
	}
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if !strings.Contains(err.Error(), "manifest media type differs from its descriptor") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
