// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestPullMetadataTLSCertificateFailuresAreNotRetryable(t *testing.T) {
	root, rootDigest := releasePublicationFixture(t)
	for _, test := range []struct {
		name, certificateHost string
		trusted, expired      bool
	}{
		{"trusted certificate", "registry.example.test", true, false},
		{"untrusted certificate", "registry.example.test", false, false},
		{"hostname mismatch", "other.example.test", true, false},
		{"expired certificate", "registry.example.test", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// These subtests remain sequential: registryOptions uses the global
			// default transport, which must be restored before parallel tests run.
			certificate := metadataRegistryTLSCertificate(t, test.certificateHost, test.expired)
			var requests atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler.ServeHTTP(w, r)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			roots := x509.NewCertPool()
			if test.trusted {
				roots.AddCert(certificate.Leaf)
			}
			original := remote.DefaultTransport
			base, ok := original.(*http.Transport)
			if !ok {
				t.Fatalf("default registry transport has unexpected type %T", original)
			}
			clientTransport := base.Clone()
			clientTransport.Proxy = nil
			clientTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			clientTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, server.Listener.Addr().String())
			}
			remote.DefaultTransport = clientTransport
			defer func() {
				remote.DefaultTransport = original
				clientTransport.CloseIdleConnections()
			}()
			// A non-loopback logical host forces HTTPS; only its TCP destination
			// is redirected. Normal chain, validity and hostname checks stay on.
			const repository = "registry.example.test/product/releases"
			reference := repository + "@" + rootDigest
			options := RegistryOptions{Anonymous: true}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if test.name == "trusted certificate" {
				published, err := Push(ctx, root, repository, options)
				if err != nil || published != reference {
					t.Fatalf("publish trusted TLS fixture: reference=%q, error=%v", published, err)
				}
				requests.Store(0)
			}
			workspace := t.TempDir()
			got, err := PullMetadata(ctx, reference, filepath.Join(workspace, "metadata"), options)
			if test.name == "trusted certificate" {
				if err != nil || got.RootDigest != rootDigest || requests.Load() == 0 {
					t.Fatalf("trusted TLS acquisition: root=%q, requests=%d, error=%v", got.RootDigest, requests.Load(), err)
				}
				return
			}
			var verification *tls.CertificateVerificationError
			if !errors.As(err, &verification) {
				t.Fatalf("TLS acquisition lost the certificate rejection: %v", err)
			}
			switch test.name {
			case "untrusted certificate":
				var unknown x509.UnknownAuthorityError
				if !errors.As(verification.Err, &unknown) {
					t.Fatalf("untrusted certificate failed for another reason: %v", verification.Err)
				}
			case "hostname mismatch":
				var hostname x509.HostnameError
				if !errors.As(verification.Err, &hostname) {
					t.Fatalf("hostname mismatch failed for another reason: %v", verification.Err)
				}
			case "expired certificate":
				var invalid x509.CertificateInvalidError
				if !errors.As(verification.Err, &invalid) || invalid.Reason != x509.Expired {
					t.Fatalf("expired certificate failed for another reason: %v", verification.Err)
				}
			default:
				t.Fatalf("unexpected certificate failure case %q", test.name)
			}
			if requests.Load() != 0 {
				t.Fatalf("rejected certificate reached the HTTP handler %d times", requests.Load())
			}
			if !reflect.DeepEqual(got, Inspection{}) {
				t.Fatalf("rejected certificate returned partial metadata: %+v", got)
			}
			entries, readErr := os.ReadDir(workspace)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("rejected certificate exposed output or staging: %v, %v", entries, readErr)
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeFetchFailed || e.ExitCode(err) != 20 || e.Retryable(err) {
				t.Fatalf("certificate rejection must be nonretryable fetch failure/exit20: exit=%d, retryable=%t, error=%v", e.ExitCode(err), e.Retryable(err), err)
			}
		})
	}
}

func metadataRegistryTLSCertificate(t *testing.T, host string, expired bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{host},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if expired {
		template.NotAfter = now.Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}
