// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestChunkArchiveBoundsAndExactSections(t *testing.T) {
	for _, tc := range []struct {
		name             string
		size, chunkBytes int64
		lengths          []int64
	}{
		{"one byte", 1, 1024, []int64{1}},
		{"exact chunk", 1024, 1024, []int64{1024}},
		{"partial final chunk", 2049, 1024, []int64{1024, 1024, 1}},
		{"maximum count", 64, 1, slices.Repeat([]int64{1}, 64)},
		{"empty", 0, 1024, nil},
		{"negative", -1, 1024, nil},
		{"aggregate overflow", transportarchive.MaxArchiveBytes + 1, maxChunkBytes, nil},
		{"chunk overflow", 1, maxChunkBytes + 1, nil},
		{"zero chunk", 1, 0, nil},
		{"negative chunk", 1, -1, nil},
		{"excessive count", 65, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lengths == nil {
				layers, digest, err := chunkArchive(t.Context(), nil, tc.size, tc.chunkBytes)
				assertCode(t, err, e.CodeVerifyFailed)
				if len(layers) != 0 || digest != "" {
					t.Fatal("invalid archive returned partial integrity metadata")
				}
				return
			}
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 251)
			}
			layers, digest, err := chunkArchive(t.Context(), bytes.NewReader(data), tc.size, tc.chunkBytes)
			if err != nil || len(layers) != len(tc.lengths) {
				t.Fatalf("chunking failed: %d layers, %v", len(layers), err)
			}
			want := sha256.Sum256(data)
			if digest != "sha256:"+hex.EncodeToString(want[:]) {
				t.Fatal("archive hash does not bind original bytes")
			}
			var assembled bytes.Buffer
			for i, layer := range layers {
				size, err := layer.Size()
				if err != nil || size != tc.lengths[i] {
					t.Fatalf("chunk %d has size %d: %v", i, size, err)
				}
				for attempt := range 2 {
					reader, err := layer.Compressed()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := io.ReadAll(reader)
					closeErr := reader.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("chunk read failed: %v %v", err, closeErr)
					}
					hash := sha256.Sum256(raw)
					digest, _ := layer.Digest()
					if digest.Hex != hex.EncodeToString(hash[:]) {
						t.Fatal("chunk reader did not reopen its exact bytes")
					}
					if attempt == 0 {
						assembled.Write(raw)
					}
				}
			}
			if !bytes.Equal(assembled.Bytes(), data) {
				t.Fatal("chunk boundaries dropped or duplicated bytes")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := chunkArchive(ctx, bytes.NewReader([]byte{1}), 1, maxChunkBytes)
	assertCode(t, err, e.CodeTimeout)
	_, _, err = chunkArchive(t.Context(), bytes.NewReader([]byte{1}), 2, maxChunkBytes)
	assertCode(t, err, e.CodeVerifyFailed)
}

func boundedManifest(sizes ...int64) *v1.Manifest {
	hash := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}
	m := &v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1, Config: v1.Descriptor{MediaType: types.OCIConfigJSON, Digest: hash, Size: 100}}
	for _, size := range sizes {
		m.Layers = append(m.Layers, v1.Descriptor{MediaType: chunkMediaType, Digest: hash, Size: size})
	}
	return m
}

func TestChunkManifestCapacityAndUntrustedDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int64
		make  func() *v1.Manifest
		valid bool
	}{
		{"one chunk", maxChunkBytes, func() *v1.Manifest { return boundedManifest(maxChunkBytes) }, true},
		{"full 64 GiB capacity", transportarchive.MaxArchiveBytes, func() *v1.Manifest { return boundedManifest(slices.Repeat([]int64{maxChunkBytes}, maxChunkCount)...) }, true},
		{"small ordered chunks", 3, func() *v1.Manifest { return boundedManifest(1, 2) }, true},
		{"nil", 1, func() *v1.Manifest { return nil }, false},
		{"empty", 1, func() *v1.Manifest { return boundedManifest() }, false},
		{"65 chunks", 65, func() *v1.Manifest { return boundedManifest(slices.Repeat([]int64{1}, 65)...) }, false},
		{"10 GB legacy layer", 10 << 30, func() *v1.Manifest { return boundedManifest(10 << 30) }, false},
		{"oversized layer", maxChunkBytes + 1, func() *v1.Manifest { return boundedManifest(maxChunkBytes + 1) }, false},
		{"missing bytes", 3, func() *v1.Manifest { return boundedManifest(1, 1) }, false},
		{"extra bytes", 1, func() *v1.Manifest { return boundedManifest(1, 1) }, false},
		{"negative size", 1, func() *v1.Manifest { return boundedManifest(-1, 2) }, false},
		{"zero size", 1, func() *v1.Manifest { return boundedManifest(0, 1) }, false},
		{"overflow", 1, func() *v1.Manifest { return boundedManifest(1<<63 - 1) }, false},
		{"gzip media", 1, func() *v1.Manifest { m := boundedManifest(1); m.Layers[0].MediaType = types.OCILayer; return m }, false},
		{"external source", 1, func() *v1.Manifest {
			m := boundedManifest(1)
			m.Layers[0].URLs = []string{"https://outside.example/payload"}
			return m
		}, false},
		{"inline payload", 1, func() *v1.Manifest { m := boundedManifest(1); m.Layers[0].Data = []byte{1}; return m }, false},
		{"wrong digest algorithm", 1, func() *v1.Manifest { m := boundedManifest(1); m.Layers[0].Digest.Algorithm = "sha512"; return m }, false},
		{"oversized config", 1, func() *v1.Manifest { m := boundedManifest(1); m.Config.Size = MaxLocatorBytes*2 + 1; return m }, false},
		{"external config", 1, func() *v1.Manifest {
			m := boundedManifest(1)
			m.Config.URLs = []string{"https://outside.example/config"}
			return m
		}, false},
		{"inline config", 1, func() *v1.Manifest { m := boundedManifest(1); m.Config.Data = []byte{1}; return m }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChunkManifest(tc.make(), tc.size)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func chunkFixture(t *testing.T) (Locator, []v1.Layer, v1.Image, *v1.Manifest) {
	t.Helper()
	root := t.TempDir()
	writePayload(t, root, "payload/bin/tool", strings.Repeat("binary bytes ", 180), 0o751)
	writePayload(t, root, "payload/data", "metadata", 0o640)
	archive := filepath.Join(root, "archive.tar")
	if err := transportarchive.Pack(t.Context(), archive, root, []string{"payload"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	layers, digest, err := chunkArchive(t.Context(), file, info.Size(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) < 3 {
		t.Fatal("fixture did not span several chunks")
	}
	l := Locator{SchemaVersion: SchemaVersion, Request: testRequest, ArchiveDigest: digest, ArchiveSize: info.Size(), Paths: []string{"payload"}, StripPrefix: "payload"}
	image, err := carrierImage(layers, l)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	l.Reference = testRequest.packageName() + "@" + hash.String()
	manifest, err := image.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	return l, layers, image, manifest
}

func TestPrivateCarrierMultipleRawChunksThroughRegistry(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	l, _, image, _ := chunkFixture(t)
	ref, err := name.NewDigest(l.Reference, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image, f.b.registryOptions(t.Context(), testOptions)...); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := f.b.download(t.Context(), testRequest, l, destination, testOptions); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, "bin/tool"))
	if err != nil || string(raw) != strings.Repeat("binary bytes ", 180) {
		t.Fatalf("multi-chunk archive did not restore: %v", err)
	}
	assertCode(t, f.b.download(t.Context(), testRequest, l, destination, testOptions), e.CodeImmutableViolation)
}

type editedChunk struct {
	v1.Layer
	open func() (io.ReadCloser, error)
}

func (l editedChunk) Compressed() (io.ReadCloser, error) { return l.open() }

type observedChunkReader struct {
	io.Reader
	closed     *bool
	closeError error
}

func (r observedChunkReader) Close() error { *r.closed = true; return r.closeError }

func TestChunkFailuresNeverPublishAndCloseReaders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		edit       func([]byte) []byte
		cancel     bool
		closeError bool
		code       e.Code
	}{
		{"corrupted", func(raw []byte) []byte { raw[0] ^= 1; return raw }, false, false, e.CodeVerifyFailed},
		{"truncated", func(raw []byte) []byte { return raw[:len(raw)-1] }, false, false, e.CodeVerifyFailed},
		{"extra byte", func(raw []byte) []byte { return append(raw, 1) }, false, false, e.CodeVerifyFailed},
		{"close failure", func(raw []byte) []byte { return raw }, false, true, e.CodeFetchFailed},
		{"canceled", func(raw []byte) []byte { return raw }, true, false, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, layers, _, manifest := chunkFixture(t)
			reader, err := layers[0].Compressed()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(reader)
			if closeErr := reader.Close(); err != nil || closeErr != nil {
				t.Fatalf("reading fixture: %v %v", err, closeErr)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closed, secondOpened := false, false
			layers[0] = editedChunk{Layer: layers[0], open: func() (io.ReadCloser, error) {
				if tc.cancel {
					cancel()
				}
				var closeErr error
				if tc.closeError {
					closeErr = errors.New("reader close failure")
				}
				return observedChunkReader{Reader: bytes.NewReader(tc.edit(raw)), closed: &closed, closeError: closeErr}, nil
			}}
			second := layers[1]
			layers[1] = editedChunk{Layer: second, open: func() (io.ReadCloser, error) { secondOpened = true; return second.Compressed() }}
			destination := t.TempDir()
			assertCode(t, restoreLayers(ctx, layers, manifest, l, destination), tc.code)
			if !closed || secondOpened {
				t.Fatal("failed chunk was not closed or subsequent payload was opened")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed chunks exposed partial filesystem output: %v", err)
			}
		})
	}
}

func TestChunkReorderingAndArchiveBindingFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Locator, []v1.Layer, *v1.Manifest)
	}{
		{"layer substitution", func(_ *Locator, layers []v1.Layer, _ *v1.Manifest) { layers[0], layers[1] = layers[1], layers[0] }},
		{"coherent reorder", func(_ *Locator, layers []v1.Layer, manifest *v1.Manifest) {
			layers[0], layers[1] = layers[1], layers[0]
			manifest.Layers[0], manifest.Layers[1] = manifest.Layers[1], manifest.Layers[0]
		}},
		{"whole archive digest", func(l *Locator, _ []v1.Layer, _ *v1.Manifest) { l.ArchiveDigest = "sha256:" + strings.Repeat("0", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, layers, _, manifest := chunkFixture(t)
			tc.edit(&l, layers, manifest)
			destination := t.TempDir()
			assertCode(t, restoreLayers(t.Context(), layers, manifest, l, destination), e.CodeVerifyFailed)
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid chunks published output: %v", err)
			}
		})
	}
}

func TestPrivateCarrierRejectsChunkConfigMismatch(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	l, _, image, _ := chunkFixture(t)
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.RootFS.DiffIDs[0] = v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("0", 64)}
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	l.Reference = testRequest.packageName() + "@" + digest.String()
	ref, err := name.NewDigest(l.Reference, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image, f.b.registryOptions(t.Context(), testOptions)...); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	assertCode(t, f.b.download(t.Context(), testRequest, l, destination, testOptions), e.CodeVerifyFailed)
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unbound chunk config published output: %v", err)
	}
}

type chunkRoundTripper func(*http.Request) (*http.Response, error)

func (f chunkRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrivateCarrierCancellationJoinsBlockedChunkFetch(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	l, _, image, manifest := chunkFixture(t)
	ref, err := name.NewDigest(l.Reference, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image, f.b.registryOptions(t.Context(), testOptions)...); err != nil {
		t.Fatal(err)
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	base := f.transport.base
	f.transport.base = chunkRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+manifest.Layers[0].Digest.String()) {
			close(started)
			defer close(stopped)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return base.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	destination := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- f.b.download(ctx, testRequest, l, destination, testOptions) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("chunk fetch did not start")
	}
	cancel()
	select {
	case err := <-done:
		assertCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled carrier did not join chunk fetch")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("download returned before chunk request stopped")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled chunk left partial output: %v", err)
	}
}
