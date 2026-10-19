// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/net/http2"
)

type checkedPullResetTransport struct {
	http.RoundTripper
	payload         string
	reads           atomic.Int32
	closed          atomic.Bool
	cause, closeErr error
}

func (transport *checkedPullResetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	if err == nil && request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/blobs/"+transport.payload) {
		if transport.reads.Add(1) == 1 {
			response.Body = checkedPullResetBody{ReadCloser: response.Body, closed: &transport.closed, cause: transport.cause, closeErr: transport.closeErr}
		}
	}
	return response, err
}

type checkedPullResetBody struct {
	io.ReadCloser
	closed          *atomic.Bool
	cause, closeErr error
}

func (body checkedPullResetBody) Read([]byte) (int, error) {
	return 0, body.cause
}

func (body checkedPullResetBody) Close() error {
	body.closed.Store(true)
	return errors.Join(body.ReadCloser.Close(), body.closeErr)
}

func TestCheckedPublicationRetriesOnlyInterruptedOutput(t *testing.T) {
	root, payload := checkedLateDiskFixture(t)
	peer := http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}
	for _, test := range []struct {
		name            string
		cause, closeErr error
		code            e.Code
	}{
		{"peer reset", peer, nil, ""},
		{"integrity failure", e.New(e.CodeVerifyFailed, "checksum mismatch"), nil, e.CodeVerifyFailed},
		{"body close failure", peer, errors.New("close failed"), e.CodeVerifyFailed},
		{"filesystem failure", &os.PathError{Op: "write", Path: "payload", Err: errors.New("no space left")}, nil, e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			// The adapter injects a typed peer reset into an actual registry response body.
			// All manifests, final payload bytes and ownership checks use the real readers.
			original := remote.DefaultTransport
			transport := &checkedPullResetTransport{RoundTripper: original, payload: payload, cause: test.cause, closeErr: test.closeErr}
			remote.DefaultTransport = transport
			t.Cleanup(func() { remote.DefaultTransport = original })
			workspace := t.TempDir()
			checked, err := PullCheckedPublication(t.Context(), reference, workspace, options)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				if checked != nil || transport.reads.Load() != 1 || !transport.closed.Load() {
					t.Fatalf("permanent failure retried or issued owner: reads=%d closed=%v owner=%v", transport.reads.Load(), transport.closed.Load(), checked)
				}
				assertEmptyPullDirectory(t, workspace)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer checked.Close()
			if transport.reads.Load() != 2 || !transport.closed.Load() {
				t.Fatalf("interrupted body was not closed and reacquired once: reads=%d closed=%v", transport.reads.Load(), transport.closed.Load())
			}
			if len(checked.state.outputs) != 2 || len(checked.state.executables) != 1 {
				t.Fatal("retry repeated prior output observations or executable ownership")
			}
			seen := map[string]bool{}
			for _, file := range checked.state.files {
				if seen[file.Digest()] {
					t.Fatal("retry retained duplicate files")
				}
				seen[file.Digest()] = true
			}
			assertEmptyPullDirectory(t, workspace)
		})
	}
}

func TestCheckedPublicationRejectsInvalidPackageContents(t *testing.T) {
	for _, format := range []string{"apt-repository"} {
		t.Run(format, func(t *testing.T) {
			fixture := packagePullFixture(t, format, "invalid contents")
			server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			checked, err := PullCheckedPublication(t.Context(), reference, workspace, options)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if checked != nil {
				t.Fatal("self-pinned malformed package issued owner")
			}
			assertEmptyPullDirectory(t, workspace)
		})
	}
}
