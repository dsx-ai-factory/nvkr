package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestBinaryPinningSidecarAndFallback(t *testing.T) {
	const payload = "downloaded binary fixture"
	want := pipeline.FileDigest([]byte(payload))
	for _, tc := range []struct {
		name, sidecar string
		status        int
		download      bool
	}{
		{"checksum only", strings.TrimPrefix(want, "sha256:"), http.StatusOK, false},
		{"checksum with filename", strings.TrimPrefix(want, "sha256:") + "  binary\n", http.StatusOK, false},
		{"missing sidecar", "", http.StatusNotFound, true},
		{"malformed sidecar", "not a digest", http.StatusOK, true},
		{"empty sidecar", "", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/binary.sha256":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.sidecar))
				case "/binary":
					downloads.Add(1)
					_, _ = w.Write([]byte(payload))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			got, err := resolveBinaryDigest(context.Background(), server.URL+"/binary")
			if err != nil || got != want {
				t.Fatalf("digest = %q, err = %v", got, err)
			}
			count := int32(0)
			if tc.download {
				count = 1
			}
			if downloads.Load() != count {
				t.Fatalf("artifact downloads = %d, want %d", downloads.Load(), count)
			}
		})
	}
}

func TestCatalogHTTPReadsAreBoundedAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		chunked, truncated bool
		code               e.Code
	}{
		{name: "exact limit", body: "1234", status: http.StatusOK},
		{name: "declared oversize", body: "12345", status: http.StatusOK, code: e.CodeFetchFailed},
		{name: "streamed oversize", body: "12345", status: http.StatusOK, chunked: true, code: e.CodeFetchFailed},
		{name: "truncated body", body: "12", status: http.StatusOK, truncated: true, code: e.CodeFetchFailed},
		{name: "not found", status: http.StatusNotFound, code: e.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.truncated {
					w.Header().Set("Content-Length", "4")
				}
				w.WriteHeader(tc.status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			body, bodyErr := httpGetBody(context.Background(), server.URL, 4)
			digest, digestErr := httpSHA256(context.Background(), server.URL, 4)
			if tc.code != "" {
				assertLifecycleCommandCode(t, bodyErr, tc.code)
				assertLifecycleCommandCode(t, digestErr, tc.code)
				if len(body) != 0 || digest != "" {
					t.Fatal("failed read exposed partial data")
				}
				return
			}
			if bodyErr != nil || digestErr != nil || string(body) != tc.body || digest != pipeline.FileDigest([]byte(tc.body)) {
				t.Fatalf("body %q (%v), digest %q (%v)", body, bodyErr, digest, digestErr)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolveBinaryDigest(ctx, "http://must-not-be-contacted.invalid/binary")
	assertLifecycleCommandCode(t, err, e.CodeTimeout)
	_, err = httpGetBody(context.Background(), ":bad url", 4)
	assertLifecycleCommandCode(t, err, e.CodeFetchFailed)
}
