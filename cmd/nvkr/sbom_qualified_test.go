// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestDefaultEvidenceHTTPClientHasExplicitDeadlines(t *testing.T) {
	client := newEvidenceHTTPClient()
	if client.Timeout <= 0 || client.Timeout > 10*time.Minute {
		t.Fatalf("evidence client timeout = %s, want a positive bounded deadline", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout <= 0 || transport.TLSHandshakeTimeout <= 0 {
		t.Fatalf("evidence transport lacks bounded header/TLS deadlines: %#v", client.Transport)
	}
}

func TestEvidenceReaderCancellationIsCodedBeforeTransport(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("transport must not run")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (commandEvidenceReader{client: client}).ReadEvidence(ctx, "https://evidence.example.test/sbom", sbom.MaxBundleBytes)
	if cliErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("canceled evidence read code = %q, want %q: %v",
			cliErrorCode(err), nvkrerr.CodeTimeout, err)
	}
	if called {
		t.Fatal("canceled evidence read reached the HTTP transport")
	}
}

func TestEvidenceReaderCancellationStopsBoundedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingEvidenceReader{cancel: cancel}
	_, err := readBoundedEvidence(ctx, reader, sbom.MaxBundleBytes)
	if cliErrorCode(err) != nvkrerr.CodeTimeout || !nvkrerr.Retryable(err) {
		t.Fatalf("mid-read cancellation error = %v, want retryable timeout", err)
	}
	if reader.reads != 1 {
		t.Fatalf("evidence reader performed %d reads after cancellation, want one", reader.reads)
	}
}

func TestEvidenceReaderRejectsSymlinkAndFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (commandEvidenceReader{}).ReadEvidence(context.Background(), "file://"+link, sbom.MaxBundleBytes); cliErrorCode(err) != nvkrerr.CodeUnavailable {
		t.Fatalf("symlink evidence error = %v, want unavailable", err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (commandEvidenceReader{}).ReadEvidence(context.Background(), "file://"+fifo, sbom.MaxBundleBytes); cliErrorCode(err) != nvkrerr.CodeUnavailable {
		t.Fatalf("FIFO evidence error = %v, want unavailable", err)
	}
}

func TestEvidenceReaderHonorsRequestedLimit(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(evidence, []byte("pinned release archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(len("pinned release archive"))
	for _, tc := range []struct {
		name  string
		limit int64
		want  nvkrerr.Code
	}{
		{name: "exact limit", limit: size},
		{name: "one byte over", limit: size - 1, want: nvkrerr.CodeUnavailable},
		{name: "no limit", limit: 0, want: nvkrerr.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := (commandEvidenceReader{}).ReadEvidence(context.Background(), "file://"+evidence, tc.limit)
			if cliErrorCode(err) != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if tc.want == "" && int64(len(raw)) != size {
				t.Fatalf("read %d bytes, want %d", len(raw), size)
			}
		})
	}
	if _, err := readBoundedEvidence(context.Background(), strings.NewReader("pinned release archive"), size-1); cliErrorCode(err) != nvkrerr.CodeUnavailable {
		t.Fatalf("bounded stream over its limit: %v", err)
	}
}

func TestEvidenceReaderHonorsRequestedLimitOverHTTPS(t *testing.T) {
	const body = "pinned release archive"
	size := int64(len(body))
	for _, tc := range []struct {
		name          string
		contentLength int64
		limit         int64
		want          nvkrerr.Code
	}{
		{name: "exact limit", contentLength: size, limit: size},
		{name: "advertised length over limit", contentLength: size, limit: size - 1, want: nvkrerr.CodeUnavailable},
		{name: "unknown length over limit", contentLength: -1, limit: size - 1, want: nvkrerr.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responseBody := &closeRecordingBody{Reader: strings.NewReader(body)}
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: tc.contentLength,
					Body: responseBody, Request: request}, nil
			})}
			raw, err := (commandEvidenceReader{client: client}).ReadEvidence(context.Background(),
				"https://evidence.example.test/archive.tar.gz", tc.limit)
			if cliErrorCode(err) != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if tc.want == "" && string(raw) != body {
				t.Fatalf("read %q, want %q", raw, body)
			}
			if !responseBody.closed {
				t.Fatal("evidence response body was not closed")
			}
		})
	}
}

type closeRecordingBody struct {
	*strings.Reader
	closed bool
}

func (body *closeRecordingBody) Close() error {
	body.closed = true
	return nil
}

func TestEvidenceBundleVerifierWithoutDigestNamesTheFlag(t *testing.T) {
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	for _, f := range qualifiedDocumentVerifierFlags() {
		if err := f.Apply(flags); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := evidenceBundleVerifier(cli.NewContext(&cli.App{}, flags, nil))
	if err != nil {
		t.Fatal(err)
	}
	err = verifier.VerifyEvidenceBundle(context.Background(), []byte("{}"), sbom.EvidenceBundlePolicy{SubjectDigest: "sha256:" + strings.Repeat("a", 64)})
	if cliErrorCode(err) != nvkrerr.CodeUnavailable || !strings.Contains(err.Error(), "--cosign-digest") {
		t.Fatalf("missing verifier error = %v, want %q naming --cosign-digest", err, nvkrerr.CodeUnavailable)
	}
}

type cancelingEvidenceReader struct {
	cancel context.CancelFunc
	reads  int
}

func (reader *cancelingEvidenceReader) Read(buffer []byte) (int, error) {
	reader.reads++
	n := copy(buffer, "one evidence chunk")
	reader.cancel()
	return n, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
