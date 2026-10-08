// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

type releaseExecutableRoundTripper func(*http.Request) (*http.Response, error)

func (f releaseExecutableRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestReleaseExecutableProductionDispatch(t *testing.T) {
	executable := []byte("independently pinned executable")
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "bin/tool", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(executable))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{name: "exact"}, {name: "HTTPS redirect"},
		{name: "404", code: e.CodeFetchFailed},
		{name: "HTTP redirect", code: e.CodeFetchFailed},
		{name: "userinfo redirect", code: e.CodeFetchFailed},
		{name: "redirect loop", code: e.CodeFetchFailed},
		{name: "client error", code: e.CodeFetchFailed},
		{name: "canceled", code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			var unsafeRequests atomic.Int32
			unsafeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				unsafeRequests.Add(1)
				_, _ = w.Write(archive.Bytes())
			})
			plainTarget := httptest.NewServer(unsafeHandler)
			defer plainTarget.Close()
			userTarget := httptest.NewTLSServer(unsafeHandler)
			defer userTarget.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/final" {
					_, _ = w.Write(archive.Bytes())
					return
				}
				switch tc.name {
				case "404":
					w.WriteHeader(http.StatusNotFound)
				case "HTTP redirect":
					http.Redirect(w, r, plainTarget.URL+"/archive", http.StatusFound)
				case "userinfo redirect":
					http.Redirect(w, r, strings.Replace(userTarget.URL, "https://", "https://user@", 1)+"/archive", http.StatusFound)
				case "redirect loop":
					http.Redirect(w, r, "/archive", http.StatusFound)
				case "HTTPS redirect":
					http.Redirect(w, r, "/final", http.StatusFound)
				default:
					_, _ = w.Write(archive.Bytes())
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.name == "canceled" {
				cancel()
			}
			transport := server.Client().Transport
			if tc.name == "client error" {
				transport = releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
					return nil, fmt.Errorf("injected transport failure")
				})
			}
			node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: server.URL + "/archive",
				Digest: sha256Of(executable), Platform: "linux/amd64", Network: ir.NetworkDefault,
				OutputType: "host-package", ArtifactPath: "tool", Version: "1.2.3",
				Env:               []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}},
				ExecutableArchive: &ir.ExecutableArchive{Digest: sha256Of(archive.Bytes()), Member: "bin/tool"}}
			opts := RunOptions{Ctx: ctx, SourceRoot: filepath.Join(t.TempDir(), "sources"), Stdout: io.Discard, releaseExecutableTransport: transport}
			root := prebuiltDir(opts, node)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			prior := []byte("previous output")
			if err := os.WriteFile(filepath.Join(root, "tool"), prior, 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (Local{}).pullBinary(node, opts)
			if code(err) != tc.code {
				t.Fatalf("result=%+v error=%v, want %s", result, err, tc.code)
			}
			if tc.name == "HTTP redirect" || tc.name == "userinfo redirect" {
				if unsafeRequests.Load() != 0 || err == nil || !strings.Contains(err.Error(), "unsafe release executable redirect") {
					t.Fatalf("unsafe redirect guard did not reject before contacting live target: requests=%d error=%v", unsafeRequests.Load(), err)
				}
			}
			want := prior
			if tc.code == "" {
				want = executable
				if result == nil || result.SHA256 != node.Digest || result.SHA256 == node.ExecutableArchive.Digest || result.Size != int64(len(executable)) {
					t.Fatalf("dispatch did not preserve executable identity: %+v", result)
				}
			} else if result != nil {
				t.Fatal("failed dispatch returned a result")
			}
			got, err := os.ReadFile(filepath.Join(root, "tool"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("output=%q error=%v", got, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "tool" {
				t.Fatalf("archive carrier escaped into output root: %v, %v", entries, err)
			}
			if tc.name == "redirect loop" && requests.Load() != 10 {
				t.Fatalf("redirect limit admitted %d requests, want 10", requests.Load())
			}
			if tc.name == "canceled" && requests.Load() != 0 {
				t.Fatal("canceled acquisition contacted upstream")
			}
		})
	}
}

type releaseExecutableZeroReader struct{}

func (releaseExecutableZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestReleaseExecutableCarrierFailureCodes(t *testing.T) {
	const transportLimit = 256 << 20
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{name: "truncated body", code: e.CodeFetchFailed},
		{name: "oversized streaming body", code: e.CodeFetchFailed},
		{name: "archive digest mismatch", code: e.CodeVerifyFailed},
		{name: "mid-body cancellation", code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.name == "truncated body" {
					w.Header().Set("Content-Length", "100000")
				}
				_, _ = w.Write([]byte("short"))
			}))
			defer server.Close()
			transport := server.Client().Transport
			if tc.name == "oversized streaming body" || tc.name == "mid-body cancellation" {
				transport = releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
					body := io.LimitReader(releaseExecutableZeroReader{}, transportLimit+1)
					if tc.name == "mid-body cancellation" {
						body = releaseExecutableRoundTripReader(func(p []byte) (int, error) {
							cancel()
							return copy(p, "short"), context.Canceled
						})
					}
					return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(body)}, nil
				})
			}
			node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: server.URL + "/archive",
				Digest: sha256Of([]byte("executable")), Platform: "linux/amd64", Network: ir.NetworkDefault,
				OutputType: "host-package", ArtifactPath: "tool", Version: "1.2.3",
				Env:               []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}},
				ExecutableArchive: &ir.ExecutableArchive{Digest: sha256Of([]byte("expected carrier")), Member: "bin/tool"}}
			root := t.TempDir()
			prior := []byte("previous executable")
			if err := os.WriteFile(filepath.Join(root, "tool"), prior, 0600); err != nil {
				t.Fatal(err)
			}
			digest, size, err := downloadReleaseExecutableWithTransport(ctx, node, root, "tool", transport)
			if code(err) != tc.code || digest != "" || size != 0 {
				t.Fatalf("failure returned %s/%d, %v; want %s", digest, size, err, tc.code)
			}
			got, readErr := os.ReadFile(filepath.Join(root, "tool"))
			entries, listErr := os.ReadDir(root)
			if readErr != nil || !bytes.Equal(got, prior) || listErr != nil || len(entries) != 1 {
				t.Fatalf("failure changed existing output or left carrier: bytes=%q entries=%v errors=%v/%v", got, entries, readErr, listErr)
			}
		})
	}
}

type releaseExecutableRoundTripReader func([]byte) (int, error)

func (reader releaseExecutableRoundTripReader) Read(p []byte) (int, error) { return reader(p) }

func TestReleaseExecutableDefaultClientPreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := downloadReleaseExecutableWithTransport(ctx, ir.Node{ExecutableArchive: &ir.ExecutableArchive{}}, t.TempDir(), "tool", nil)
	if code(err) != e.CodeTimeout {
		t.Fatalf("default production client cancellation: %v", err)
	}
}

func TestReleaseExecutableCarrierIsNotPublishedDuringAcquisition(t *testing.T) {
	root := t.TempDir()
	archive := []byte("authenticated but invalid tar-gzip carrier")
	node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: "https://example.invalid/archive",
		Digest: sha256Of([]byte("executable")), Platform: "linux/amd64", Network: ir.NetworkDefault,
		OutputType: "host-package", ArtifactPath: "tool", Version: "1.2.3",
		Env:               []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}},
		ExecutableArchive: &ir.ExecutableArchive{Digest: sha256Of(archive), Member: "bin/tool"}}
	transport := releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("archive carrier pathname published before acquisition: %v, %v", entries, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive))}, nil
	})
	digest, size, err := downloadReleaseExecutableWithTransport(t.Context(), node, root, "tool", transport)
	if code(err) != e.CodeVerifyFailed || digest != "" || size != 0 {
		t.Fatalf("malformed authenticated carrier returned output: %s/%d, %v", digest, size, err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed acquisition left output-root scratch: %v, %v", entries, readErr)
	}
}
