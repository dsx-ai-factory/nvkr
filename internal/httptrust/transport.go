// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package httptrust

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateCertificateAuthority checks only the authored optional PEM selection;
// it does not load system roots or authorize a network request.
func ValidateCertificateAuthority(certificateAuthority string) error {
	if certificateAuthority != "" && (len(certificateAuthority) > 64<<10 || !x509.NewCertPool().AppendCertsFromPEM([]byte(certificateAuthority))) {
		return e.New(e.CodeTrustPolicyMissing, "isolated HTTPS requires a bounded explicit PEM certificate authority")
	}
	return nil
}

// NewTransport owns direct HTTPS transport trust for explicit provider routes. An explicit Plan-bound CA
// replaces system roots; otherwise ambient certificate overrides are refused.
// Proxies and mutable default transports never select this authority route.
func NewTransport(ctx context.Context, certificateAuthority string) (*http.Transport, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "isolated HTTP transport requires a context")
	}
	if ctx.Err() != nil {
		return nil, e.New(e.CodeTimeout, "isolated HTTP transport context is canceled")
	}
	var roots *x509.CertPool
	if certificateAuthority != "" {
		roots = x509.NewCertPool()
		if len(certificateAuthority) > 64<<10 || !roots.AppendCertsFromPEM([]byte(certificateAuthority)) {
			return nil, e.New(e.CodeTrustPolicyMissing, "isolated HTTP requires a bounded explicit PEM certificate authority")
		}
	} else {
		if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
			return nil, e.New(e.CodeTrustPolicyMissing, "isolated HTTP certificate overrides require an authored certificateAuthority")
		}
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, e.New(e.CodeTrustPolicyMissing, "isolated HTTP system certificate roots are unavailable")
		}
	}
	if ctx.Err() != nil {
		return nil, e.New(e.CodeTimeout, "isolated HTTP transport context is canceled")
	}
	return &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}, nil
}
