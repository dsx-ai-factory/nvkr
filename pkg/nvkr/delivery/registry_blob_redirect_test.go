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
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullRegistryBlobRedirectIntegrity(t *testing.T) {
	if mode := os.Getenv("NVKR_REGISTRY_BLOB_REDIRECT_CHILD"); mode != "" {
		testPullRegistryBlobRedirectChild(t, mode)
		return
	}
	for _, mode := range []string{"success", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			publication, built := registryPublicationFixture(t)
			inspection, err := Inspect(t.Context(), publication)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(fixtureBlobPath(publication, inspection.Root.Objects[0].DescriptorDigest))
			if err != nil {
				t.Fatal(err)
			}
			var index ociIndex
			if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) != 1 {
				t.Fatalf("fixture needs one nested executable manifest: %v", err)
			}
			raw, err = os.ReadFile(fixtureBlobPath(publication, index.Manifests[0].Digest))
			if err != nil {
				t.Fatal(err)
			}
			var manifest ociManifest
			if err := json.Unmarshal(raw, &manifest); err != nil || len(manifest.Layers) != 1 {
				t.Fatalf("fixture needs one actual executable payload: %v", err)
			}
			payloadDigest := manifest.Layers[0].Digest
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			seed := httptest.NewServer(handler)
			defer seed.Close()
			if _, err := Push(t.Context(), publication, strings.TrimPrefix(seed.URL, "http://")+"/release", RegistryOptions{Anonymous: true, PlainHTTP: true}); err != nil {
				t.Fatal(err)
			}

			certificate := blobRedirectCertificate(t)
			var storageReads, payloadReads, registryAuth, tokenAuth atomic.Int64
			storage := blobRedirectServer(t, certificate, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				storageReads.Add(1)
				for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "PRIVATE-TOKEN", "JOB-TOKEN"} {
					if r.Header.Get(key) != "" {
						t.Errorf("storage received credential header %s", key)
					}
				}
				digest := strings.TrimPrefix(r.URL.Path, "/object/")
				body, err := os.ReadFile(fixtureBlobPath(publication, digest))
				if err != nil {
					http.NotFound(w, r)
					return
				}
				if digest == payloadDigest && r.Method == http.MethodGet {
					payloadReads.Add(1)
					if mode == "corrupt" {
						body[len(body)/2] ^= 0xff // Preserve size; only the byte identity changes.
					}
				}
				_, _ = w.Write(body)
			}))
			defer storage.Close()
			origin := blobRedirectServer(t, certificate, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/jwt/auth" {
					user, password, ok := r.BasicAuth()
					if !ok || user != "gitlab-ci-token" || password != "synthetic-registry-secret" {
						t.Error("token endpoint did not receive the selected credential")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					tokenAuth.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"token": "synthetic-registry-bearer", "expires_in": 3600})
					return
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-registry-bearer" {
					w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/jwt/auth",service="container_registry"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				registryAuth.Add(1)
				if digest, ok := strings.CutPrefix(r.URL.Path, "/v2/release/blobs/"); ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
					http.Redirect(w, r, storage.URL+"/object/"+digest+"?synthetic-signature=value", http.StatusTemporaryRedirect)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer origin.Close()

			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestPullRegistryBlobRedirectIntegrity$", "-test.count=1")
			child.Env = []string{"GODEBUG=x509usefallbackroots=1", "NVKR_REGISTRY_BLOB_REDIRECT_CHILD=" + mode, "NVKR_REGISTRY_BLOB_REDIRECT_ORIGIN=" + origin.URL, "NVKR_REGISTRY_BLOB_REDIRECT_CA=" + ca, "NVKR_REGISTRY_BLOB_REDIRECT_DIGEST=" + built.RootDigest}
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("production isolated TLS Pull: %v\n%s", err, output)
			}
			if storageReads.Load() == 0 || payloadReads.Load() == 0 || registryAuth.Load() == 0 || tokenAuth.Load() == 0 {
				t.Fatalf("production readback/authentication path was not exercised: storage=%d payload=%d registry=%d token=%d", storageReads.Load(), payloadReads.Load(), registryAuth.Load(), tokenAuth.Load())
			}
		})
	}
}

func testPullRegistryBlobRedirectChild(t *testing.T, mode string) {
	t.Helper()
	if mode != "success" && mode != "corrupt" {
		t.Fatal("unknown fixture mode")
	}
	raw, err := os.ReadFile(os.Getenv("NVKR_REGISTRY_BLOB_REDIRECT_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		t.Fatal("fixture CA is invalid")
	}
	// Fallback roots affect this disposable process only. Production Pull still
	// constructs its own isolated transport; no default transport is replaced.
	x509.SetFallbackRoots(roots)
	origin := os.Getenv("NVKR_REGISTRY_BLOB_REDIRECT_ORIGIN")
	host := strings.TrimPrefix(origin, "https://")
	options := RegistryOptions{Username: "gitlab-ci-token", Password: "synthetic-registry-secret", IsolatedTransport: true,
		Authority: &RegistryAuthority{RegistryHost: host, TokenEndpoint: origin + "/jwt/auth", Repositories: []string{host + "/release"}}}
	workspace := t.TempDir()
	out := filepath.Join(workspace, "accepted")
	want := os.Getenv("NVKR_REGISTRY_BLOB_REDIRECT_DIGEST")
	inspection, err := Pull(t.Context(), host+"/release@"+want, out, options)
	if mode == "corrupt" {
		assertRegistryCode(t, err, e.CodeUnavailable)
		// The pinned registry reader's checksum error is in an upstream
		// internal package, so inspect its concrete type without importing it.
		// A timeout, auth refusal or unrelated availability error cannot pass.
		checksumFailure := false
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			kind := reflect.TypeOf(cause)
			if kind.PkgPath() == "github.com/google/go-containerregistry/internal/verify" && kind.Name() == "Error" {
				checksumFailure = true
			}
		}
		if !checksumFailure || !reflect.DeepEqual(inspection, Inspection{}) {
			t.Fatalf("corrupt redirected payload was accepted: inspection=%+v err=%v", inspection, err)
		}
		assertEmptyPullDirectory(t, workspace)
		return
	}
	if err != nil || inspection.RootDigest != want {
		t.Fatalf("admitted redirect failed production readback: digest=%s err=%v", inspection.RootDigest, err)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	if _, err := ExtractBinaryBundle(t.Context(), out, "tools", extracted); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(extracted, "tool"))
	if err != nil || string(payload) != "exact executable bytes" {
		t.Fatalf("production Pull changed executable bytes: %q %v", payload, err)
	}
}

func blobRedirectCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func blobRedirectServer(t *testing.T, certificate tls.Certificate, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	// This equivalent loopback literal keeps the registry library's implicit
	// plaintext rule for the exact 127.0.0.1 spelling out of an HTTPS test.
	// The production transport still dials the real local socket and checks TLS.
	server.URL = "https://[::ffff:127.0.0.1]:" + port
	repository, err := name.NewRepository(strings.TrimPrefix(server.URL, "https://")+"/release", name.StrictValidation)
	if err != nil || repository.Scheme() != "https" {
		server.Close()
		t.Fatalf("fixture must select HTTPS in the pinned registry name owner: %v", err)
	}
	return server
}
