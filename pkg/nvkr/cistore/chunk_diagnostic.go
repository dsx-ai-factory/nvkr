// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/net/http2"
)

const maxChunkDiagnosticNodes = 32
const maxChunkDiagnosticBytes = 4096

// The raw provider error is never retained: even its wrapped URL, HTTP body or
// TLS certificate description can contain sensitive data. The private marker
// distinguishes a narrowly admitted body-read interruption from public codes.
type chunkTransferError struct {
	safe          error
	retryBodyRead bool
}

func (err *chunkTransferError) Error() string { return err.safe.Error() }
func (err *chunkTransferError) Unwrap() error { return err.safe }

// Only the exact stream failure can authorize an extraction retry. An enclosing
// cleanup/verification error or a joined error cannot inherit this permission.
func isRetryableChunkRead(err error) bool {
	failure, ok := err.(*chunkTransferError)
	return ok && failure.retryBodyRead
}

type chunkDiagnostic struct {
	category  string
	code      e.Code
	transient bool
}

func (r *chunkReader) ioFailure(operation string, cause error) error {
	var diagnostic chunkDiagnostic
	if err := r.ctx.Err(); err != nil {
		diagnostic = chunkContextDiagnostic(err)
	} else {
		budget := maxChunkDiagnosticNodes
		diagnostic = classifyChunkCause(cause, &budget)
	}
	// Failure to close a response is a failed cleanup boundary, not permission
	// for a caller to retry the transfer. Cancellation retains its public code.
	if operation == "closing" && diagnostic.category != "context-canceled" && diagnostic.category != "context-deadline" {
		diagnostic.code = e.CodeFetchFailed
	}
	descriptor := r.descriptors[r.index]
	return &chunkTransferError{
		safe: e.New(diagnostic.code, "%s private CI chunk (diagnostic: %s; chunk=%d/%d; read=%d/%d)",
			operation, diagnostic.category, r.index+1, len(r.descriptors), descriptor.Size-r.remaining, descriptor.Size),
		retryBodyRead: operation == "reading" && diagnostic.transient,
	}
}

func chunkContextDiagnostic(cause error) chunkDiagnostic {
	category := "context-canceled"
	if cause == context.DeadlineExceeded {
		category = "context-deadline"
	}
	return chunkDiagnostic{category: category, code: e.CodeTimeout}
}

// Traversal and fallback text inspection are bounded. Known wrappers are
// unwrapped without formatting them; URL wrappers are never treated as network
// outages merely because they implement net.Error. Text is diagnostic-only and
// cannot mint the typed transient marker.
func classifyChunkCause(cause error, remaining *int) chunkDiagnostic {
	unknown := chunkDiagnostic{category: "unknown", code: e.CodeFetchFailed}
	if cause == nil {
		return unknown
	}
	if *remaining == 0 {
		return chunkDiagnostic{category: "error-chain-limit", code: e.CodeFetchFailed}
	}
	*remaining--
	switch cause {
	case context.Canceled, context.DeadlineExceeded:
		return chunkContextDiagnostic(cause)
	case io.ErrUnexpectedEOF:
		return chunkDiagnostic{category: "unexpected-eof", code: e.CodeUnavailable, transient: true}
	case io.EOF:
		return chunkDiagnostic{category: "unexpected-eof", code: e.CodeUnavailable}
	case syscall.ECONNRESET:
		return chunkDiagnostic{category: "connection-reset", code: e.CodeUnavailable, transient: true}
	case syscall.ETIMEDOUT:
		return chunkDiagnostic{category: "network-timeout", code: e.CodeTimeout, transient: true}
	case syscall.ECONNREFUSED:
		return chunkDiagnostic{category: "connection-refused", code: e.CodeUnavailable}
	case syscall.EPIPE, syscall.ECONNABORTED, net.ErrClosed:
		return chunkDiagnostic{category: "connection-closed", code: e.CodeUnavailable}
	case http.ErrSchemeMismatch:
		return chunkDiagnostic{category: "http-protocol", code: e.CodeFetchFailed}
	}
	switch typed := cause.(type) {
	case *tls.CertificateVerificationError, x509.CertificateInvalidError,
		x509.HostnameError, x509.UnknownAuthorityError, x509.SystemRootsError:
		return chunkDiagnostic{category: "tls-verification", code: e.CodeFetchFailed}
	case tls.RecordHeaderError:
		return chunkDiagnostic{category: "http-protocol", code: e.CodeFetchFailed}
	case *os.PathError, *os.LinkError:
		return chunkDiagnostic{category: "filesystem-failure", code: e.CodeFetchFailed}
	case *transport.Error:
		return chunkHTTPDiagnostic(typed.StatusCode)
	case *url.Error:
		return classifyChunkCause(typed.Err, remaining)
	case *net.OpError:
		diagnostic := classifyChunkCause(typed.Err, remaining)
		if diagnostic.category == "unknown" {
			return chunkDiagnostic{category: "network-failure", code: e.CodeUnavailable}
		}
		return diagnostic
	case *e.Error:
		if typed.Code == e.CodeVerifyFailed || typed.Code == e.CodeImmutableViolation {
			return chunkDiagnostic{category: "integrity-rejected", code: typed.Code}
		}
		// A previous coded boundary owns its failure. Never recover merely
		// because its wrapped cause happens to be a transient network error.
		for _, code := range e.AllCodes {
			if typed.Code == code {
				return chunkDiagnostic{category: "boundary-rejected", code: code}
			}
		}
		return chunkDiagnostic{category: "boundary-rejected", code: e.CodeFetchFailed}
	}
	if joined, ok := cause.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 || len(children) > *remaining {
			return chunkDiagnostic{category: "error-chain-limit", code: e.CodeFetchFailed}
		}
		var result chunkDiagnostic
		for _, child := range children {
			diagnostic := classifyChunkCause(child, remaining)
			if diagnostic.category == "context-canceled" || diagnostic.category == "context-deadline" {
				return diagnostic
			}
			if result.category == "" || result.transient && !diagnostic.transient {
				result = diagnostic
			}
		}
		// Independent failures cannot jointly authorize replay, even when
		// each individual cause would admit a fresh body read.
		result.transient = false
		return result
	}
	if wrapped, ok := cause.(interface{ Unwrap() error }); ok {
		return classifyChunkCause(wrapped.Unwrap(), remaining)
	}
	var stream http2.StreamError
	// Go 1.27's net/http stream errors convert to this public type. Only
	// inspect the leaf after the bounded wrapper/independent-failure checks;
	// errors.As on the original chain could bypass a rejecting boundary.
	if errors.As(cause, &stream) {
		return chunkHTTP2Diagnostic(stream)
	}
	if network, ok := cause.(net.Error); ok {
		if network.Timeout() {
			return chunkDiagnostic{category: "network-timeout", code: e.CodeTimeout, transient: true}
		}
		return chunkDiagnostic{category: "network-failure", code: e.CodeUnavailable}
	}
	message := cause.Error()
	if len(message) > maxChunkDiagnosticBytes {
		return chunkDiagnostic{category: "oversized-diagnostic", code: e.CodeFetchFailed}
	}
	if chunkChecksumMessage.MatchString(message) {
		return chunkDiagnostic{category: "digest-mismatch", code: e.CodeVerifyFailed}
	}
	if chunkSizeMessage.MatchString(message) {
		return chunkDiagnostic{category: "size-mismatch", code: e.CodeVerifyFailed}
	}
	if strings.HasPrefix(message, "stream error: stream ID ") || strings.HasPrefix(message, "http2: ") {
		return chunkDiagnostic{category: "http2-protocol", code: e.CodeFetchFailed}
	}
	return unknown
}

func chunkHTTP2Diagnostic(stream http2.StreamError) chunkDiagnostic {
	diagnostic := chunkDiagnostic{category: "http2-stream", code: e.CodeFetchFailed}
	// The typed error's fixed cause identifies a received RST_STREAM. Local
	// header/trailer violations and arbitrary lookalike error strings must
	// never grant recovery. Retain only an allowlisted diagnostic category.
	if stream.StreamID == 0 || stream.Cause == nil || stream.Cause.Error() != "received from peer" {
		return diagnostic
	}
	switch stream.Code {
	case http2.ErrCodeProtocol:
		diagnostic.category = "http2-peer-protocol-reset"
	case http2.ErrCodeInternal:
		diagnostic.category = "http2-peer-internal-reset"
	case http2.ErrCodeRefusedStream:
		diagnostic.category = "http2-peer-refused-stream"
	default:
		return diagnostic
	}
	diagnostic.code, diagnostic.transient = e.CodeUnavailable, true
	return diagnostic
}

// These anchored forms belong to the exact pinned go-containerregistry verifier.
// A matching untyped error is still never eligible for extraction retry.
var chunkChecksumMessage = regexp.MustCompile(`^error verifying sha256 checksum after reading [0-9]+ bytes; got "sha256:[a-f0-9]{64}", want "sha256:[a-f0-9]{64}"$`)
var chunkSizeMessage = regexp.MustCompile(`^error verifying size; got [0-9]+, want [0-9]+$`)

func chunkHTTPDiagnostic(status int) chunkDiagnostic {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return chunkDiagnostic{category: "registry-auth", code: e.CodeFetchFailed}
	case status == http.StatusNotFound:
		return chunkDiagnostic{category: "registry-missing", code: e.CodeFetchFailed}
	case status == http.StatusTooManyRequests:
		return chunkDiagnostic{category: "registry-rate-limit", code: e.CodeUnavailable}
	case status == http.StatusRequestTimeout:
		return chunkDiagnostic{category: "registry-timeout", code: e.CodeUnavailable}
	case status >= 500 && status < 600:
		return chunkDiagnostic{category: "registry-unavailable", code: e.CodeUnavailable}
	default:
		return chunkDiagnostic{category: "registry-response", code: e.CodeFetchFailed}
	}
}
