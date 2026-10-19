// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestMetadataFetchErrorDistinguishesRequestRejectionFromOutage(t *testing.T) {
	certificate := &x509.Certificate{DNSNames: []string{"registry.example.test"}}
	verification := &tls.CertificateVerificationError{Err: errors.New("certificate policy rejected")}
	coded := e.Wrap(e.CodeVerifyFailed, verification, "metadata trust check failed")
	policyRejection := errors.New("redirect policy refused request")
	dialFailure := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	networkTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	nestedRequest := func(cause error) error {
		return fmt.Errorf("redirected request: %w", &url.Error{
			Op: "Get", URL: "https://registry.example.test/redirected", Err: cause,
		})
	}
	for _, test := range []struct {
		name      string
		cause     error
		code      e.Code
		exit      int
		retryable bool
		stream    bool
		canceled  bool
	}{
		{name: "service unavailable", cause: &transport.Error{StatusCode: http.StatusServiceUnavailable}, code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "request timeout", cause: &transport.Error{StatusCode: http.StatusRequestTimeout}, code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "rate limited", cause: &transport.Error{StatusCode: http.StatusTooManyRequests}, code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "forbidden", cause: &transport.Error{StatusCode: http.StatusForbidden}, code: e.CodeFetchFailed, exit: 20},
		{name: "not found", cause: &transport.Error{StatusCode: http.StatusNotFound}, code: e.CodeFetchFailed, exit: 20},
		{name: "TLS verification rejection", cause: verification, code: e.CodeFetchFailed, exit: 20},
		{name: "expired certificate", cause: x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired}, code: e.CodeFetchFailed, exit: 20},
		{name: "hostname mismatch", cause: x509.HostnameError{Certificate: certificate, Host: "other.example.test"}, code: e.CodeFetchFailed, exit: 20},
		{name: "unknown authority", cause: x509.UnknownAuthorityError{Cert: certificate}, code: e.CodeFetchFailed, exit: 20},
		{name: "unavailable trust roots", cause: x509.SystemRootsError{Err: errors.New("trust roots unavailable")}, code: e.CodeFetchFailed, exit: 20},
		{name: "HTTP response to HTTPS request", cause: http.ErrSchemeMismatch, code: e.CodeFetchFailed, exit: 20},
		{name: "redirect policy rejection", cause: policyRejection, code: e.CodeFetchFailed, exit: 20},
		{name: "nested redirect rejection", cause: nestedRequest(policyRejection), code: e.CodeFetchFailed, exit: 20},
		{name: "dial refused", cause: dialFailure, code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "connection reset", cause: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "network timeout", cause: networkTimeout, code: e.CodeTimeout, exit: 31, retryable: true},
		{name: "nested dial refused", cause: nestedRequest(dialFailure), code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "nested network timeout", cause: nestedRequest(networkTimeout), code: e.CodeTimeout, exit: 31, retryable: true},
		{name: "nested truncated stream", cause: nestedRequest(io.ErrUnexpectedEOF), code: e.CodeUnavailable, exit: 30, retryable: true},
		{name: "stream EOF", cause: io.EOF, code: e.CodeUnavailable, exit: 30, retryable: true, stream: true},
		{name: "truncated stream", cause: io.ErrUnexpectedEOF, code: e.CodeUnavailable, exit: 30, retryable: true, stream: true},
		{name: "wrapped deadline", cause: context.DeadlineExceeded, code: e.CodeTimeout, exit: 31, retryable: true},
		{name: "existing integrity classification", cause: coded, code: e.CodeVerifyFailed, exit: 22},
		{name: "cancellation takes precedence", cause: coded, code: e.CodeTimeout, exit: 31, retryable: true, canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// HTTP request errors wrap their cause in url.Error, which satisfies
			// net.Error even for a permanent request rejection. Body reads
			// report their stream error directly instead.
			upstream := test.cause
			if !test.stream {
				upstream = fmt.Errorf("registry request: %w", &url.Error{
					Op: "Get", URL: "https://registry.example.test/v2/", Err: test.cause,
				})
			}
			if test.canceled {
				cancel()
			}
			got := metadataFetchError(ctx, upstream, "fetch metadata index")
			assertRegistryCode(t, got, test.code)
			if e.ExitCode(got) != test.exit || e.Retryable(got) != test.retryable {
				t.Fatalf("exit=%d retryable=%t, want exit=%d retryable=%t: %v", e.ExitCode(got), e.Retryable(got), test.exit, test.retryable, got)
			}
			if test.canceled {
				if !errors.Is(got, context.Canceled) {
					t.Fatalf("cancellation cause was lost: %v", got)
				}
			} else if !errors.Is(got, upstream) || !errors.Is(got, test.cause) {
				t.Fatalf("registry failure lost its original cause: %v", got)
			}
		})
	}
}
