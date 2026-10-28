// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"golang.org/x/net/http2"
)

func TestOCIBlobTransportRecoversPeerInternalStreamError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		failures, want int32
		status         int
	}{
		{"one reset", 1, 2, http.StatusOK},
		{"persistent reset", 3, 3, 0},
		{"forbidden", 0, 1, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("protocol = %s, want HTTP/2", r.Proto)
				}
				if requests.Add(1) <= tc.failures {
					// net/http sends a real peer RST_STREAM(INTERNAL_ERROR).
					panic(http.ErrAbortHandler)
				}
				w.WriteHeader(tc.status)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			client := server.Client()
			client.Transport = ociBlobTransport(client.Transport)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v2/", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if response != nil {
				defer response.Body.Close()
			}
			if tc.status == 0 {
				var stream http2.StreamError
				if !errors.As(err, &stream) || stream.Code != http2.ErrCodeInternal {
					t.Fatalf("persistent reset = %v", err)
				}
			} else if err != nil || response.StatusCode != tc.status {
				t.Fatalf("response = %v, error = %v", response, err)
			}
			if got := requests.Load(); got != tc.want {
				t.Fatalf("requests = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOCIBlobTransportDoesNotRetryUnrelatedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"lookalike text", errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer")},
		{"certificate", x509.UnknownAuthorityError{}},
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"other stream code", http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			base := releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, tc.err
			})
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://fixture.invalid/v2/", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := ociBlobTransport(base).RoundTrip(request)
			if response != nil {
				defer response.Body.Close()
			}
			if !errors.Is(err, tc.err) || requests != 1 {
				t.Fatalf("requests = %d, error = %v", requests, err)
			}
		})
	}
}

func TestPullOCIArchiveAnonymousBearerAndTerminalVerification(t *testing.T) {
	body := ociChartArchive(t)
	digest := sha256Of(body)
	var tokenRequests, blobRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="chart-fixture"`, server.URL))
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			tokenRequests.Add(1)
			if r.Header.Get("Authorization") != "" || r.URL.Query().Get("scope") != "repository:charts/postgresql:pull" {
				t.Errorf("authentication request inherited credentials or wrong scope: %s %q", r.URL, r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"token":"fixture-token","expires_in":600}`)
		case "/v2/charts/postgresql/blobs/" + digest:
			blobRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("blob request lacks bearer/byte preservation headers: %v", r.Header)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected request (must never resolve manifest/tag): %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	node := ociBlobPullNode(strings.TrimPrefix(server.URL, "http://"), digest)
	graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: node.Platform, Nodes: []ir.Node{
		node, {ID: "verify", Kind: ir.KindVerify, Needs: []string{node.ID}},
	}}
	report, err := (Local{}).Run(graph, mustGraphDigest(t, graph), RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(t.TempDir(), "src")})
	if err != nil {
		t.Fatal(err)
	}
	if tokenRequests.Load() == 0 || blobRequests.Load() != 1 || len(report.Artifacts) != 1 {
		t.Fatalf("unexpected acquisitions: tokens=%d blobs=%d artifacts=%d", tokenRequests.Load(), blobRequests.Load(), len(report.Artifacts))
	}
	artifact := report.Artifacts[0]
	actual, err := os.ReadFile(artifact.AbsPath)
	if err != nil || !bytes.Equal(actual, body) || artifact.SHA256 != digest || artifact.Size != int64(len(body)) || artifact.OutputType != "raw" || artifact.EngineClass != ir.EnginePrebuiltAcquire || artifact.Packaged {
		t.Fatalf("archive transformed or misclassified: %+v, bytes=%d, err=%v", artifact, len(actual), err)
	}
}

func TestDownloadOCIBlobRejectsBadBytesWithoutPublishing(t *testing.T) {
	body := ociChartArchive(t)
	digest := sha256Of(body)
	for _, tc := range []struct {
		name  string
		serve func(http.ResponseWriter, *http.Request)
		code  e.Code
	}{
		{"mismatch", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("wrong archive")) }, e.CodeVerifyFailed},
		{"missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, e.CodeFetchFailed},
		{"oversize header", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(maxOCIArchiveBytes+1))
		}, e.CodeFetchFailed},
		{"truncated", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "4096")
			_, _ = w.Write(body[:16])
		}, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				tc.serve(w, r)
			}))
			defer server.Close()
			root := t.TempDir()
			destination := filepath.Join(root, "chart.tgz")
			if err := os.WriteFile(destination, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			node := ociBlobPullNode(strings.TrimPrefix(server.URL, "http://"), digest)
			_, _, err := downloadOCIBlobVerify(t.Context(), node.Artifact, root, "chart.tgz", digest)
			if code(err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
			actual, err := os.ReadFile(destination)
			if err != nil || string(actual) != "previous" {
				t.Fatalf("prior destination changed: %q %v", actual, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("private files leaked: %v %v", entries, err)
			}
		})
	}
}

func TestDownloadOCIBlobAdmissionAndCancellation(t *testing.T) {
	digest := sha256Of([]byte("never completes"))
	for _, tc := range []struct{ name, artifact, output string }{
		{"tag", "oci-blob://must-not-contact.invalid/charts/postgresql:16.2.0", "raw"},
		{"image", "oci-blob://must-not-contact.invalid/charts/postgresql@" + digest, "oci-image"},
		{"unknown output", "oci-blob://must-not-contact.invalid/charts/postgresql@" + digest, "unknown"},
		{"unknown OCI type", "oci-manifest://must-not-contact.invalid/charts/postgresql@" + digest, "raw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			node := ociBlobPullNode("must-not-contact.invalid", digest)
			node.Artifact, node.OutputType = tc.artifact, tc.output
			_, err := (Local{}).pull(node, RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(root, "src"), Stdout: io.Discard})
			if err == nil {
				t.Fatal("invalid transport/output admitted")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("admission failure touched filesystem: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := downloadOCIBlobVerify(ctx, ociBlobPullNode("must-not-contact.invalid", digest).Artifact, t.TempDir(), "chart", digest)
	if code(err) != e.CodeTimeout {
		t.Fatalf("canceled acquisition = %v", err)
	}

	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte("first bytes"))
		w.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	node := ociBlobPullNode(strings.TrimPrefix(server.URL, "http://"), digest)
	result := make(chan error, 1)
	go func() { _, _, err := downloadOCIBlobVerify(ctx, node.Artifact, root, "chart", digest); result <- err }()
	select {
	case <-started:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("download did not start")
	}
	select {
	case err := <-result:
		if code(err) != e.CodeTimeout {
			t.Fatalf("canceled body = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download did not stop")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled download leaked output: %v %v", entries, err)
	}
}

func TestVerifiedDownloadEnforcesStreamBound(t *testing.T) {
	root := t.TempDir()
	body := bytes.Repeat([]byte("a"), 1025)
	_, _, err := writeVerifiedDownload(t.Context(), bytes.NewReader(body), "fixture", root, "archive", sha256Of(body), 1024)
	if code(err) != e.CodeFetchFailed {
		t.Fatalf("over-limit stream = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized stream leaked output: %v %v", entries, err)
	}
}

func ociBlobPullNode(registry, digest string) ir.Node {
	return ir.Node{ID: "pull-postgresql", Kind: ir.KindPull, Component: "postgresql", OutputType: "raw", Network: ir.NetworkDefault,
		Platform: "linux/amd64", Artifact: ir.OCIBlobScheme + registry + "/charts/postgresql@" + digest, Digest: digest, ArtifactPath: "postgresql", Version: "16.2.0"}
}

func ociChartArchive(t *testing.T) []byte {
	t.Helper()
	var archive bytes.Buffer
	zipped := gzip.NewWriter(&archive)
	tarred := tar.NewWriter(zipped)
	chart := []byte("apiVersion: v2\nname: postgresql\nversion: 16.2.0\n")
	if err := tarred.WriteHeader(&tar.Header{Name: "postgresql/Chart.yaml", Mode: 0o644, Size: int64(len(chart))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarred.Write(chart); err != nil {
		t.Fatal(err)
	}
	if err := tarred.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
