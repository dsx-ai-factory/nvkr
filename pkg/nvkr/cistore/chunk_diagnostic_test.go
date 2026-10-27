// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestChunkDiagnosticsRetainOnlySafeCategories(t *testing.T) {
	secret := "credential-marker"
	wrapped := func(cause error) error {
		return fmt.Errorf("%s: %w", secret, &url.Error{Op: "Get", URL: "https://user:" + secret + "@example.test/blob?sig=" + secret, Err: cause})
	}
	requestURL, err := url.Parse("https://user:" + secret + "@example.test/blob?sig=" + secret)
	if err != nil {
		t.Fatal(err)
	}
	registry := func(status int) error {
		return &transport.Error{StatusCode: status, Request: &http.Request{Method: "GET", URL: requestURL},
			Errors: []transport.Diagnostic{{Message: secret, Detail: secret}}}
	}
	for _, test := range []struct {
		name, category string
		cause          error
		code           e.Code
		retry          bool
	}{
		{"truncated response", "unexpected-eof", wrapped(io.ErrUnexpectedEOF), e.CodeUnavailable, true},
		{"EOF", "unexpected-eof", wrapped(io.EOF), e.CodeUnavailable, false},
		{"reset", "connection-reset", wrapped(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), e.CodeUnavailable, true},
		{"timeout", "network-timeout", wrapped(&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}), e.CodeTimeout, true},
		{"refused", "connection-refused", wrapped(syscall.ECONNREFUSED), e.CodeUnavailable, false},
		{"dial failure", "network-failure", wrapped(&net.OpError{Op: "dial", Err: errors.New(secret)}), e.CodeUnavailable, false},
		{"context canceled", "context-canceled", wrapped(context.Canceled), e.CodeTimeout, false},
		{"context deadline", "context-deadline", wrapped(context.DeadlineExceeded), e.CodeTimeout, false},
		{"certificate", "tls-verification", wrapped(&tls.CertificateVerificationError{Err: syscall.ECONNRESET}), e.CodeFetchFailed, false},
		{"wrong protocol", "http-protocol", wrapped(http.ErrSchemeMismatch), e.CodeFetchFailed, false},
		{"server unavailable", "registry-unavailable", registry(503), e.CodeUnavailable, false},
		{"rate limit", "registry-rate-limit", registry(429), e.CodeUnavailable, false},
		{"forbidden", "registry-auth", registry(403), e.CodeFetchFailed, false},
		{"missing", "registry-missing", registry(404), e.CodeFetchFailed, false},
		{"bad digest", "digest-mismatch", errors.New(`error verifying sha256 checksum after reading 100 bytes; got "sha256:` + strings.Repeat("a", 64) + `", want "sha256:` + strings.Repeat("b", 64) + `"`), e.CodeVerifyFailed, false},
		{"bad size", "size-mismatch", errors.New("error verifying size; got 20, want 100"), e.CodeVerifyFailed, false},
		{"HTTP2", "http2-protocol", errors.New("stream error: stream ID 3; INTERNAL_ERROR; " + secret), e.CodeFetchFailed, false},
		{"unknown URL rejection", "unknown", wrapped(errors.New(secret)), e.CodeFetchFailed, false},
		{"untrusted timeout text", "unknown", errors.New("context deadline exceeded: " + secret), e.CodeFetchFailed, false},
		{"oversized", "oversized-diagnostic", errors.New(strings.Repeat(secret, maxChunkDiagnosticBytes)), e.CodeFetchFailed, false},
		{"cyclic wrapper", "error-chain-limit", &cyclicChunkCause{}, e.CodeFetchFailed, false},
		{"joined unknown", "unknown", errors.Join(io.ErrUnexpectedEOF, errors.New(secret)), e.CodeFetchFailed, false},
		{"joined certificate", "tls-verification", errors.Join(syscall.ECONNRESET, &tls.CertificateVerificationError{Err: errors.New(secret)}), e.CodeFetchFailed, false},
		{"joined context", "context-canceled", errors.Join(io.ErrUnexpectedEOF, context.Canceled), e.CodeTimeout, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &chunkReader{ctx: t.Context(), descriptors: []v1.Descriptor{{Size: 100}}, remaining: 60}
			err := reader.ioFailure("reading", test.cause)
			assertCode(t, err, test.code)
			if !strings.Contains(err.Error(), "diagnostic: "+test.category+";") ||
				!strings.Contains(err.Error(), "chunk=1/1; read=40/100") {
				t.Fatalf("lost bounded diagnostic: %v", err)
			}
			if isRetryableChunkRead(err) != test.retry {
				t.Fatalf("retry marker differs: %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example.test") || len(err.Error()) > 512 {
				t.Fatal("unsafe or oversized diagnostic escaped")
			}
			if errors.Is(err, test.cause) {
				t.Fatal("raw provider error remains reachable")
			}
		})
	}
}

type cyclicChunkCause struct{}

func (*cyclicChunkCause) Error() string     { panic("wrappers must not be formatted") }
func (err *cyclicChunkCause) Unwrap() error { return err }

func TestChunkDiagnosticsDoNotRecoverCodedBoundaryFailures(t *testing.T) {
	for _, code := range e.AllCodes {
		t.Run(string(code), func(t *testing.T) {
			reader := &chunkReader{ctx: t.Context(), descriptors: []v1.Descriptor{{Size: 100}}, remaining: 60}
			err := reader.ioFailure("reading", e.Wrap(code, io.ErrUnexpectedEOF, "credential-marker"))
			assertCode(t, err, code)
			if isRetryableChunkRead(err) || strings.Contains(err.Error(), "credential-marker") {
				t.Fatal("coded boundary was retried or exposed its raw message")
			}
		})
	}
}

func TestChunkDiagnosticCancellationAndOperationAdmission(t *testing.T) {
	for _, operation := range []string{"opening", "reading", "closing"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			reader := &chunkReader{ctx: ctx, descriptors: []v1.Descriptor{{Size: 100}}, remaining: 100}
			err := reader.ioFailure(operation, syscall.ECONNRESET)
			if operation == "closing" {
				assertCode(t, err, e.CodeFetchFailed)
				if e.Retryable(err) {
					t.Fatal("failed cleanup has a retryable public code")
				}
			}
			if isRetryableChunkRead(err) != (operation == "reading") {
				t.Fatal("retry marker escaped the body-read boundary")
			}
			if isRetryableChunkRead(fmt.Errorf("wrapped failure: %w", err)) || isRetryableChunkRead(errors.Join(err, errors.New("cleanup failure"))) {
				t.Fatal("a larger failure inherited retry authority")
			}
			cancel()
			err = reader.ioFailure(operation, &cyclicChunkCause{})
			assertCode(t, err, e.CodeTimeout)
			if isRetryableChunkRead(err) || !strings.Contains(err.Error(), "context-canceled") {
				t.Fatal("cancellation did not override provider diagnostics")
			}
		})
	}
}

func TestChunkReadCleanupFailureRevokesRetry(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(closeFailure), func(t *testing.T) {
			_, layers, _, manifest := chunkFixture(t)
			closed := false
			var closeErr error
			if closeFailure {
				closeErr = errors.New("credential-marker")
			}
			layers[0] = editedChunk{Layer: layers[0], open: func() (io.ReadCloser, error) {
				return observedChunkReader{Reader: failingChunkBody{}, closed: &closed, closeError: closeErr}, nil
			}}
			reader := newChunkReader(t.Context(), layers, manifest.Layers)
			_, err := io.Copy(io.Discard, reader)
			wantCode := e.CodeUnavailable
			if closeFailure {
				wantCode = e.CodeFetchFailed
			}
			assertCode(t, err, wantCode)
			if closeFailure && e.Retryable(err) {
				t.Fatal("failed cleanup retained a retryable public code")
			}
			if !closed || reader.source != nil || isRetryableChunkRead(err) == closeFailure {
				t.Fatal("failed response cleanup did not control retry admission")
			}
			if strings.Contains(err.Error(), "credential-marker") {
				t.Fatal("close failure leaked its raw provider diagnostic")
			}
		})
	}
}

type failingChunkBody struct{}

func (failingChunkBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestChunkCloseTransportFailuresAreNonretryable(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{"connection reset", syscall.ECONNRESET},
		{"network timeout", syscall.ETIMEDOUT},
	} {
		for _, atEOF := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/EOF=%t", test.name, atEOF), func(t *testing.T) {
				_, layers, _, manifest := chunkFixture(t)
				original := layers[0]
				layers[0] = editedChunk{Layer: original, open: func() (io.ReadCloser, error) {
					input, err := original.Compressed()
					if err != nil {
						return nil, err
					}
					return &chunkDiagnosticCloseFailure{ReadCloser: input, cause: test.cause}, nil
				}}
				reader := newChunkReader(t.Context(), layers[:1], manifest.Layers[:1])
				var err error
				if atEOF {
					_, err = io.Copy(io.Discard, reader)
				} else {
					if err := reader.open(); err != nil {
						t.Fatal(err)
					}
					err = reader.Close()
				}
				assertCode(t, err, e.CodeFetchFailed)
				if isRetryableChunkRead(err) || e.Retryable(err) || reader.source != nil {
					t.Fatal("response cleanup failure retained retry permission or open source")
				}
			})
		}
	}
}

type chunkDiagnosticCloseFailure struct {
	io.ReadCloser
	cause error
}

func (r *chunkDiagnosticCloseFailure) Close() error {
	return errors.Join(r.ReadCloser.Close(), r.cause)
}

func TestChunkReadCancellationCannotForwardProbeBytes(t *testing.T) {
	for _, probing := range []bool{false, true} {
		t.Run(fmt.Sprint(probing), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, layers, _, manifest := chunkFixture(t)
			closed := false
			layers[0] = editedChunk{Layer: layers[0], open: func() (io.ReadCloser, error) {
				return observedChunkReader{Reader: chunkDiagnosticReadFunc(func(p []byte) (int, error) {
					p[0] = 1
					cancel()
					return 1, syscall.ECONNRESET
				}), closed: &closed, closeError: errors.New("credential-marker")}, nil
			}}
			reader := newChunkReader(ctx, layers, manifest.Layers)
			if err := reader.open(); err != nil {
				t.Fatal(err)
			}
			if probing {
				reader.remaining = 0
			}
			buffer := make([]byte, 1)
			n, err := reader.Read(buffer)
			assertCode(t, err, e.CodeTimeout)
			wantN := 1
			if probing {
				wantN = 0
				if buffer[0] != 0 {
					t.Fatal("probe byte escaped into caller buffer")
				}
			} else if reader.remaining != manifest.Layers[0].Size-1 || !strings.Contains(err.Error(), "read=1/") {
				t.Fatal("canceled read did not account delivered bytes")
			}
			if n != wantN || isRetryableChunkRead(err) || !closed || reader.source != nil || strings.Contains(err.Error(), "credential-marker") {
				t.Fatal("canceled read violated byte, secrecy or close ownership")
			}
		})
	}
}

func TestFullChunkDigestFailurePrecedesTransientReadFailure(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, syscall.ECONNRESET} {
		t.Run(cause.Error(), func(t *testing.T) {
			_, layers, _, manifest := chunkFixture(t)
			closed := false
			layers[0] = editedChunk{Layer: layers[0], open: func() (io.ReadCloser, error) {
				return observedChunkReader{Reader: chunkDiagnosticReadFunc(func(p []byte) (int, error) {
					return copy(p, bytes.Repeat([]byte{'x'}, int(manifest.Layers[0].Size))), cause
				}), closed: &closed}, nil
			}}
			reader := newChunkReader(t.Context(), layers, manifest.Layers)
			n, err := reader.Read(make([]byte, int(manifest.Layers[0].Size)))
			assertCode(t, err, e.CodeVerifyFailed)
			if int64(n) != manifest.Layers[0].Size || !closed || isRetryableChunkRead(err) || !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatal("known full-chunk digest mismatch was hidden by transient error")
			}
		})
	}
}

type chunkDiagnosticReadFunc func([]byte) (int, error)

func (read chunkDiagnosticReadFunc) Read(p []byte) (int, error) { return read(p) }
