// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitLabTransportExplicitTrustIgnoresAmbientProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	t.Setenv("SSL_CERT_FILE", "/untrusted/ci-variable.pem")
	t.Setenv("SSL_CERT_DIR", "/untrusted/ci-variable-roots")
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	transport, err := NewTransport(t.Context(), ca)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if transport.Proxy != nil {
		t.Fatal("provider transport inherits a proxy selector")
	}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent || proxyHits.Load() != 0 {
		t.Fatal("explicit provider trust did not retain its direct route")
	}
}

func TestGitLabTransportRefusesUndeclaredCertificateOverrides(t *testing.T) {
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "/untrusted/job-variable")
			transport, err := NewTransport(t.Context(), "")
			var coded *e.Error
			if transport != nil || !errors.As(err, &coded) || coded.Code != e.CodeTrustPolicyMissing {
				t.Fatalf("expected no transport and coded trust refusal, got %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, ca string }{{"invalid", "not PEM"}, {"oversized", strings.Repeat("x", (64<<10)+1)}} {
		t.Run(tc.name, func(t *testing.T) {
			transport, err := NewTransport(t.Context(), tc.ca)
			var coded *e.Error
			if transport != nil || !errors.As(err, &coded) || coded.Code != e.CodeTrustPolicyMissing {
				t.Fatalf("expected bounded trust refusal, got %v", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	transport, err := NewTransport(canceled, "")
	var coded *e.Error
	if transport != nil || !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("canceled admission: %v", err)
	}
}

func TestCurrentProtectedBranchAndTagAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                                                                     string
		require, protected, wrongName, tag, omitTag, mismatchRef, mismatchSource bool
		want                                                                     bool
	}{
		{name: "ordinary observation", want: true},
		{name: "protected branch", require: true, protected: true, want: true},
		{name: "unprotected branch", require: true},
		{name: "foreign branch response", require: true, protected: true, wrongName: true},
		{name: "tag", tag: true},
		{name: "missing tag", omitTag: true},
		{name: "caller ref mismatch", mismatchRef: true},
		{name: "caller source mismatch", mismatchSource: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var branchHits atomic.Int32
			self := fixtureJob(3, "nvkr:finalize", "running")
			ref := "feature/topic"
			self.Ref, self.Pipeline.Ref = ref, ref
			if tc.omitTag {
				self.Tag = nil
			} else {
				self.Tag = &tc.tag
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v4/projects/1":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "ci_config_path": nil, "default_branch": "main"})
				case "/api/v4/projects/1/pipelines/2":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 2, "project_id": 1, "sha": testCommit, "ref": ref, "source": "push"})
				case "/api/v4/job":
					_ = json.NewEncoder(w).Encode(self)
				case "/api/v4/projects/1/repository/branches/feature/topic":
					branchHits.Add(1)
					if r.URL.EscapedPath() != "/api/v4/projects/1/repository/branches/feature%2Ftopic" {
						t.Error("branch was not one exact escaped operand")
					}
					name := ref
					if tc.wrongName {
						name = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "protected": tc.protected})
				case "/api/v4/projects/1/pipelines/2/jobs":
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode([]apiJob{self})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
			client, err := New(t.Context(), Config{APIURL: server.URL + "/api/v4", ProjectID: 1, JobToken: "job-secret", ReadCredential: "read-secret", ReadCredentialKind: "private-token", CertificateAuthority: ca, RequireProtectedBranch: tc.require})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			identity := id()
			identity.Ref, identity.Source = ref, "push"
			if tc.mismatchRef {
				identity.Ref = "other"
			}
			if tc.mismatchSource {
				identity.Source = "web"
			}
			receipt, err := client.Current(t.Context(), identity, []string{self.Name})
			if tc.want {
				if err != nil || receipt == nil || receipt.DefaultBranch() != "main" || receipt.CurrentIdentity().Ref != ref {
					t.Fatalf("expected authenticated branch identity: %v", err)
				}
			} else {
				var coded *e.Error
				if receipt != nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("expected no receipt and coded refusal: %v", err)
				}
			}
			if !tc.require && branchHits.Load() != 0 {
				t.Fatal("ordinary observation acquired publication branch authority")
			}
		})
	}
}
