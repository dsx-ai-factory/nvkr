// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/net/http2"
)

// releasePullBlobTransport replaces the body of each payload GET with the
// attempt's behavior over the real registry response.
type releasePullBlobTransport struct {
	http.RoundTripper
	digest string
	reads  atomic.Int32
	body   func(attempt int, real io.ReadCloser) io.ReadCloser
}

func (transport *releasePullBlobTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	if err == nil && request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/blobs/"+transport.digest) {
		response.Body = transport.body(int(transport.reads.Add(1)), response.Body)
	}
	return response, err
}

// releasePullResetBody delivers a prefix of the real bytes, then a peer reset.
type releasePullResetBody struct {
	io.ReadCloser
	remaining int
	onReset   func()
}

func (body *releasePullResetBody) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		if body.onReset != nil {
			body.onReset()
		}
		return 0, http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}
	}
	n, err := body.ReadCloser.Read(buffer[:min(len(buffer), body.remaining)])
	body.remaining -= n
	return n, err
}

type releasePullCloseFailureBody struct{ *releasePullResetBody }

func (body releasePullCloseFailureBody) Close() error {
	_ = body.releasePullResetBody.Close()
	return errors.New("synthetic response close failure")
}

type releasePullTamperedBody struct{ io.ReadCloser }

func (body releasePullTamperedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	for i := range n {
		buffer[i] ^= 0xff
	}
	return n, err
}

func TestReleasePullLayerRecoversPeerResetAndReverifies(t *testing.T) {
	payload := bytes.Repeat([]byte("nvkr disk payload "), 4096)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	reset := func(real io.ReadCloser) io.ReadCloser {
		return &releasePullResetBody{ReadCloser: real, remaining: len(payload) / 2}
	}
	for _, test := range []struct {
		name  string
		body  func(attempt int, real io.ReadCloser) io.ReadCloser
		reads int32
		code  e.Code
	}{
		{"reset mid-blob recovers", func(attempt int, real io.ReadCloser) io.ReadCloser {
			if attempt == 1 {
				return reset(real)
			}
			return real
		}, 2, ""},
		{"reacquired bytes are re-verified", func(attempt int, real io.ReadCloser) io.ReadCloser {
			if attempt == 1 {
				return reset(real)
			}
			return releasePullTamperedBody{real}
		}, 2, e.CodeVerifyFailed},
		{"exhausted retries fail closed", func(_ int, real io.ReadCloser) io.ReadCloser { return reset(real) }, pullReadAttempts, e.CodeUnavailable},
		{"reset after declared size does not reacquire", func(_ int, real io.ReadCloser) io.ReadCloser {
			return &releasePullResetBody{ReadCloser: real, remaining: len(payload)}
		}, 1, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport, reference := releasePullBlobFixture(t, payload, digest, test.body)
			workspace := t.TempDir()
			file, err := fetchPullLayer(t.Context(), reference, Descriptor{MediaType: string(types.OCILayer), Digest: digest, Size: int64(len(payload))}, workspace, RegistryOptions{PlainHTTP: true, Anonymous: true})
			if got := transport.reads.Load(); got != test.reads {
				t.Fatalf("payload reads=%d, want %d", got, test.reads)
			}
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				// The caller's private stage owns cleanup of a final failed attempt.
				if file != nil {
					t.Fatal("failed fetch returned a payload")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("recovered payload differs: err=%v", err)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 1 {
				t.Fatalf("recovery left partial downloads: %v %v", entries, err)
			}
		})
	}
}

func TestReleasePullLayerCleanupFailureRefusesReacquisition(t *testing.T) {
	payload := bytes.Repeat([]byte("nvkr disk payload "), 4096)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name string
		body func(workspace string, real io.ReadCloser) io.ReadCloser
		code e.Code
	}{
		{"response close failure", func(_ string, real io.ReadCloser) io.ReadCloser {
			return releasePullCloseFailureBody{&releasePullResetBody{ReadCloser: real, remaining: len(payload) / 2}}
		}, e.CodeVerifyFailed},
		// A non-empty directory cannot be removed even when tests run as root.
		{"partial removal failure", func(workspace string, real io.ReadCloser) io.ReadCloser {
			return &releasePullResetBody{ReadCloser: real, remaining: len(payload) / 2, onReset: func() {
				path := filepath.Join(workspace, "payload-"+strings.TrimPrefix(digest, "sha256:"))
				if err := os.Remove(path); err != nil {
					t.Error(err)
				}
				if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
					t.Error(err)
				}
			}}
		}, e.CodeImmutableViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			transport, reference := releasePullBlobFixture(t, payload, digest, func(_ int, real io.ReadCloser) io.ReadCloser {
				return test.body(workspace, real)
			})
			file, err := fetchPullLayer(t.Context(), reference, Descriptor{MediaType: string(types.OCILayer), Digest: digest, Size: int64(len(payload))}, workspace, RegistryOptions{PlainHTTP: true, Anonymous: true})
			assertRegistryCode(t, err, test.code)
			if file != nil || transport.reads.Load() != 1 {
				t.Fatalf("cleanup failure reacquired or returned a payload: reads=%d", transport.reads.Load())
			}
		})
	}
}

func TestReleasePullLayerCancellationStopsPromptly(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1<<16)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transport, reference := releasePullBlobFixture(t, payload, digest, func(_ int, real io.ReadCloser) io.ReadCloser {
		return &releasePullResetBody{ReadCloser: real, remaining: len(payload) / 2, onReset: cancel}
	})
	start := time.Now()
	_, err := fetchPullLayer(ctx, reference, Descriptor{MediaType: string(types.OCILayer), Digest: digest, Size: int64(len(payload))}, t.TempDir(), RegistryOptions{PlainHTTP: true, Anonymous: true})
	assertRegistryCode(t, err, e.CodeTimeout)
	if transport.reads.Load() != 1 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("cancellation reacquired or waited: reads=%d elapsed=%s", transport.reads.Load(), time.Since(start))
	}
}

func releasePullBlobFixture(t *testing.T, payload []byte, digest string, body func(int, io.ReadCloser) io.ReadCloser) (*releasePullBlobTransport, string) {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	repository, err := name.NewRepository(strings.TrimPrefix(server.URL, "http://")+"/product/disk", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteLayer(repository, static.NewLayer(payload, types.OCILayer)); err != nil {
		t.Fatal(err)
	}
	original := remote.DefaultTransport
	transport := &releasePullBlobTransport{RoundTripper: original, digest: digest, body: body}
	remote.DefaultTransport = transport
	t.Cleanup(func() { remote.DefaultTransport = original })
	return transport, repository.Name() + "@" + digest
}
