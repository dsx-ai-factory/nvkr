// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestOCITransportAcquiresAndReusesExactRegistryClosure(t *testing.T) {
	for _, index := range []bool{false, true} {
		label := "image"
		if index {
			label = "index"
		}
		t.Run(label, func(t *testing.T) {
			server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			ref, err := name.NewTag(strings.TrimPrefix(server.URL, "http://")+"/fixture:latest", name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			image, err := random.Image(128, 2)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := image.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if index {
				idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: image})
				digest, err = idx.Digest()
				if err != nil {
					t.Fatal(err)
				}
				err = remote.WriteIndex(ref, idx, remote.WithContext(t.Context()))
			} else {
				err = remote.Write(ref, image, remote.WithContext(t.Context()))
			}
			if err != nil {
				t.Fatal(err)
			}
			pinned := ref.Context().Name() + "@" + digest.String()
			destination := filepath.Join(t.TempDir(), "download", "layout")
			transport := registryOCITransport{}
			got, size, err := transport.Acquire(t.Context(), pinned, destination)
			if err != nil || got != digest.String() || size <= 0 {
				t.Fatalf("acquire = %q, %d, %v", got, size, err)
			}
			if err := transport.Verify(t.Context(), pinned, destination); err != nil {
				t.Fatal(err)
			}
			server.Close()
			// Existing exact bytes must be reusable while the registry is unavailable.
			again, againSize, err := transport.Acquire(t.Context(), pinned, destination)
			if err != nil || again != got || againSize != size {
				t.Fatalf("reuse = %q, %d, %v", again, againSize, err)
			}
			if err := os.WriteFile(filepath.Join(destination, "oci-layout"), []byte(`{"imageLayoutVersion":"future"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err = transport.Acquire(t.Context(), pinned, destination)
			if code(err) != e.CodeVerifyFailed {
				t.Fatalf("corrupt cached closure = %v", err)
			}
		})
	}
}

func TestOCITransportRejectsUnpinnedAndCanceledAcquisition(t *testing.T) {
	for _, ref := range []string{"example.invalid/image:latest", "example.invalid/image@sha256:abc", "example.invalid/image@@sha256:" + strings.Repeat("a", 64)} {
		t.Run(ref, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "layout")
			transport := registryOCITransport{}
			_, _, err := transport.Acquire(context.Background(), ref, destination)
			if code(err) != e.CodeUnpinnedSource {
				t.Fatalf("acquire = %v", err)
			}
			if err := transport.Verify(context.Background(), ref, destination); code(err) != e.CodeUnpinnedSource {
				t.Fatalf("verify = %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("rejected acquisition created output: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	_, _, err := (registryOCITransport{}).Acquire(ctx, "example.invalid/image@sha256:"+strings.Repeat("a", 64), filepath.Join(root, "layout"))
	if code(err) != e.CodeTimeout {
		t.Fatalf("canceled acquire = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("aborted acquire left staging files: %v", entries)
	}
}
