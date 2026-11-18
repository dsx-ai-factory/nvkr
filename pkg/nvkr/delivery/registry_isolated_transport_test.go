// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestIsolatedRegistryTransportRejectsPlainHTTP(t *testing.T) {
	if err := (RegistryOptions{IsolatedTransport: true, PlainHTTP: true}).Validate(); err == nil {
		t.Fatal("isolated transport accepted explicit plain HTTP")
	}
	request, err := http.NewRequest(http.MethodGet, "http://localhost:5000/v2/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := registryTransport(RegistryOptions{IsolatedTransport: true}).RoundTrip(request); err == nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal("isolated transport accepted an HTTP request or redirect")
	}
	_, err = PushWithOptions(context.Background(), "absent-layout", "localhost:5000/fixture/release", PushOptions{Registry: RegistryOptions{Anonymous: true, IsolatedTransport: true}})
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeUsage, "expected")) {
		t.Fatalf("insecure destination was not refused before layout read: %v", err)
	}
}

func TestRegistryDefaultTransportRemainsUnchanged(t *testing.T) {
	if got := registryTransport(RegistryOptions{}); got != remote.DefaultTransport {
		t.Fatal("ordinary registry caller no longer uses the upstream default")
	}
}

type registryAuthorityProbe struct{ calls int }

func (p *registryAuthorityProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	p.calls++
	if request.Body != nil {
		_ = request.Body.Close()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}
func TestRegistryAuthorityRestrictsCredentialDestinations(t *testing.T) {
	scope := &RegistryAuthority{RegistryHost: "registry.example.test", TokenEndpoint: "https://gitlab.example.test/jwt/auth", Repositories: []string{"registry.example.test/a"}}
	cases := []struct {
		name, endpoint, authorization, host string
		allowed                             bool
	}{
		{"registry bearer", "https://registry.example.test/v2/a/manifests/x", "Bearer synthetic-bearer", "", true},
		{"token basic", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Aa%3Apull", "Basic synthetic-basic", "", true},
		{"foreign scope", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Aother%3Apush%2Cpull", "Basic synthetic-basic", "", false},
		{"combined scope", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Aa%3Apull+repository%3Aother%3Apull", "Basic synthetic-basic", "", false},
		{"blob", "https://registry.example.test/v2/a/blobs/sha256:123", "Bearer synthetic-bearer", "", true},
		{"blob upload initiation", "https://registry.example.test/v2/a/blobs/uploads/", "Bearer synthetic-bearer", "", true},
		{"nested reserved blobs repository", "https://registry.example.test/v2/a/blobs/manifests/x", "Bearer synthetic-bearer", "", false},
		{"nested reserved uploads repository", "https://registry.example.test/v2/a/blobs/uploads/blobs/x", "Bearer synthetic-bearer", "", false},
		{"nested reserved manifests repository", "https://registry.example.test/v2/a/manifests/manifests/x", "Bearer synthetic-bearer", "", false},
		{"nested reserved referrers repository", "https://registry.example.test/v2/a/referrers/manifests/x", "Bearer synthetic-bearer", "", false},
		{"nested reserved tags repository", "https://registry.example.test/v2/a/tags/manifests/x", "Bearer synthetic-bearer", "", false},
		{"blob upload", "https://registry.example.test/v2/a/blobs/uploads/session", "Bearer synthetic-bearer", "", true},
		{"referrers", "https://registry.example.test/v2/a/referrers/sha256:123", "Bearer synthetic-bearer", "", true},
		{"tags", "https://registry.example.test/v2/a/tags/list", "Bearer synthetic-bearer", "", true},
		{"nested repository", "https://registry.example.test/v2/a/child/manifests/x", "Bearer synthetic-bearer", "", false},
		{"endpoint suffix", "https://registry.example.test/v2/a/blobs-child/manifests/x", "Bearer synthetic-bearer", "", false},
		{"foreign repository", "https://registry.example.test/v2/other/manifests/x", "Bearer synthetic-bearer", "", false},
		{"dot traversal", "https://registry.example.test/v2/a/../other/manifests/x", "Bearer synthetic-bearer", "", false},
		{"foreign realm", "https://foreign.example.test/jwt/auth", "Basic synthetic-basic", "", false},
		{"foreign token path", "https://gitlab.example.test/other", "Basic synthetic-basic", "", false},
		{"basic to registry", "https://registry.example.test/v2/a/manifests/x", "Basic synthetic-basic", "", false},
		{"bearer to token endpoint", "https://gitlab.example.test/jwt/auth", "Bearer synthetic-bearer", "", false},
		{"foreign storage", "https://storage.example.test/blob", "", "", false},
		{"userinfo", "https://user@registry.example.test/v2/", "", "", false},
		{"host override", "https://registry.example.test/v2/", "", "foreign.example.test", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &registryAuthorityProbe{}
			request, err := http.NewRequest(http.MethodGet, tc.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", tc.authorization)
			if tc.host != "" {
				request.Host = tc.host
			}
			response, err := newHTTPSRegistryTransport(p, scope).RoundTrip(request)
			if response != nil {
				response.Body.Close()
			}
			if (err == nil) != tc.allowed || p.calls != boolInt(tc.allowed) {
				t.Fatalf("authority admitted=%v calls=%d error=%v", tc.allowed, p.calls, err)
			}
			if err != nil && (strings.Contains(err.Error(), "synthetic-basic") || strings.Contains(err.Error(), "synthetic-bearer")) {
				t.Fatal("credential escaped into error")
			}
		})
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func TestRegistryAuthorityRejectsWrongHostBeforeLayout(t *testing.T) {
	_, err := PushWithOptions(context.Background(), "absent-layout", "other.example.test/group/release", PushOptions{Registry: RegistryOptions{Username: "gitlab-ci-token", Password: "synthetic-secret", IsolatedTransport: true, Authority: &RegistryAuthority{RegistryHost: "registry.example.test", TokenEndpoint: "https://gitlab.example.test/jwt/auth", Repositories: []string{"registry.example.test/group/release"}}}})
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeUsage, "expected")) {
		t.Fatalf("wrong host did not fail before layout read: %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("credential escaped into error")
	}
}

func TestRegistryAuthorityRejectsDelete(t *testing.T) {
	probe := &registryAuthorityProbe{}
	scope := &RegistryAuthority{RegistryHost: "registry.example.test", TokenEndpoint: "https://gitlab.example.test/jwt/auth", Repositories: []string{"registry.example.test/a"}}
	request, err := http.NewRequest(http.MethodDelete, "https://registry.example.test/v2/a/manifests/sha256:123", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := newHTTPSRegistryTransport(probe, scope).RoundTrip(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || probe.calls != 0 {
		t.Fatal("delete request reached registry transport")
	}
}

func TestRegistryTransportFreezesAuthority(t *testing.T) {
	scope := &RegistryAuthority{
		RegistryHost: "registry.example.test", TokenEndpoint: "https://gitlab.example.test/jwt/auth",
		Repositories: []string{"registry.example.test/a", "registry.example.test/nested/b"},
	}
	probe := &registryAuthorityProbe{}
	scoped := newHTTPSRegistryTransport(probe, scope)
	// Caller changes must not alter the admitted credential destinations or scopes.
	scope.RegistryHost = "foreign.example.test"
	scope.TokenEndpoint = "https://foreign.example.test/jwt/auth"
	scope.Repositories[0] = "foreign.example.test/other"
	for _, tc := range []struct {
		name, endpoint, auth string
		allowed              bool
	}{
		{"original repository", "https://registry.example.test/v2/a/manifests/x", "Bearer synthetic", true},
		{"nested repository", "https://registry.example.test/v2/nested/b/manifests/x", "Bearer synthetic", true},
		{"repository boundary", "https://registry.example.test/v2/nested/b-sibling/manifests/x", "Bearer synthetic", false},
		{"original token scope", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Aa%3Apull", "Basic synthetic", true},
		{"nested token scope", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Anested%2Fb%3Apush%2Cpull", "Basic synthetic", true},
		{"new token scope", "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Aother%3Apull", "Basic synthetic", false},
		{"new token endpoint", "https://foreign.example.test/jwt/auth?service=container_registry&scope=repository%3Aa%3Apull", "Basic synthetic", false},
		{"new registry", "https://foreign.example.test/v2/other/manifests/x", "Bearer synthetic", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", tc.auth)
			before := probe.calls
			response, err := scoped.RoundTrip(request)
			if response != nil {
				response.Body.Close()
			}
			if (err == nil) != tc.allowed || probe.calls-before != boolInt(tc.allowed) {
				t.Fatalf("admitted=%v requests=%d: %v", tc.allowed, probe.calls-before, err)
			}
			if err != nil && e.ExitCode(err) != e.ExitCode(e.New(e.CodeTrustPolicyMissing, "expected")) {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestRegistryBlobRedirectDropsCredentialsAndRejectsOtherDelegation(t *testing.T) {
	payload := []byte("exact redirected blob")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	for _, mode := range []string{"blob", "encoded storage path", "corrupt blob", "manifest", "token", "foreign repository", "invalid digest", "http", "storage chain", "storage http chain"} {
		t.Run(mode, func(t *testing.T) {
			var storageCalls, insecureCalls, returnedRegistryCalls atomic.Int64
			var registryURL atomic.Value
			insecure := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { insecureCalls.Add(1) }))
			defer insecure.Close()
			storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				storageCalls.Add(1)
				for _, key := range []string{"Authorization", "Cookie", "PRIVATE-TOKEN", "JOB-TOKEN", "Proxy-Authorization"} {
					if r.Header.Get(key) != "" {
						t.Errorf("storage received credential header %s", key)
					}
				}
				if mode == "storage chain" {
					http.Redirect(w, r, registryURL.Load().(string)+"/v2/a/manifests/chain", http.StatusFound)
					return
				}
				if mode == "storage http chain" {
					http.Redirect(w, r, insecure.URL+"/chain", http.StatusFound)
					return
				}
				body := payload
				if mode == "corrupt blob" {
					body = []byte("substituted blob")
				}
				_, _ = w.Write(body)
			}))
			defer storage.Close()
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/a/manifests/chain" {
					returnedRegistryCalls.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/jwt/auth") && r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("registry credential missing")
				}
				location := storage.URL + "/object?synthetic-signature=value"
				if mode == "encoded storage path" {
					location = storage.URL + "/bucket%2Fobject?synthetic-signature=value"
				}
				if mode == "http" {
					location = insecure.URL + "/object"
				}
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer origin.Close()
			registryURL.Store(origin.URL)
			host := strings.TrimPrefix(origin.URL, "https://")
			scope := &RegistryAuthority{RegistryHost: host, TokenEndpoint: origin.URL + "/jwt/auth", Repositories: []string{host + "/a"}}
			client := origin.Client()
			client.Transport = newHTTPSRegistryTransport(client.Transport, scope)
			requestPath := "/v2/a/blobs/" + digest
			switch mode {
			case "manifest":
				requestPath = "/v2/a/manifests/" + digest
			case "token":
				requestPath = "/jwt/auth?service=container_registry&scope=repository%3Aa%3Apull"
			case "foreign repository":
				requestPath = "/v2/b/blobs/" + digest
			case "invalid digest":
				requestPath = "/v2/a/blobs/not-a-digest"
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin.URL+requestPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer synthetic-token")
			if mode == "token" {
				request.Header.Set("Authorization", "Basic synthetic-token")
			}
			request.Header.Set("Cookie", "synthetic-cookie=value")
			request.Header.Set("PRIVATE-TOKEN", "synthetic-private")
			request.Header.Set("JOB-TOKEN", "synthetic-job")
			response, err := client.Do(request)
			allowed := mode == "blob" || mode == "encoded storage path" || mode == "corrupt blob"
			if response != nil {
				defer response.Body.Close()
			}
			if allowed {
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				matches := fmt.Sprintf("sha256:%x", sha256.Sum256(got)) == digest
				if matches != (mode != "corrupt blob") {
					t.Fatal("redirect altered expected blob integrity")
				}
				if storageCalls.Load() != 1 {
					t.Fatal("storage delegation did not execute exactly once")
				}
			} else {
				if err == nil {
					t.Fatal("out-of-scope redirect succeeded")
				}
				wantStorage := int64(0)
				if mode == "storage chain" || mode == "storage http chain" {
					wantStorage = 1
				}
				if storageCalls.Load() != wantStorage {
					t.Fatal("unadmitted storage request reached destination")
				}
			}
			if returnedRegistryCalls.Load() != 0 {
				t.Fatal("storage redirected back into registry authority")
			}
			if insecureCalls.Load() != 0 {
				t.Fatal("redirect reached an insecure destination")
			}
		})
	}
}

// The base owns forwarded bodies; every local refusal must close its own body.
func TestRegistryLocalRefusalsCloseRequestBody(t *testing.T) {
	valid := &RegistryAuthority{RegistryHost: "registry.example.test", TokenEndpoint: "https://gitlab.example.test/jwt/auth", Repositories: []string{"registry.example.test/a"}}
	for _, tc := range []struct {
		name, endpoint, method string
		change                 func(*http.Request, *httpsRegistryTransport)
		allowed                bool
	}{
		{name: "forwarded control", endpoint: "https://registry.example.test/v2/a/blobs/uploads/", method: http.MethodPost, allowed: true},
		{name: "HTTP", endpoint: "http://registry.example.test/v2/"},
		{name: "nil URL", endpoint: "https://registry.example.test/v2/", change: func(r *http.Request, _ *httpsRegistryTransport) { r.URL = nil }},
		{name: "invalid configured scope", endpoint: "https://registry.example.test/v2/", change: func(_ *http.Request, s *httpsRegistryTransport) {
			s.admissionErr = e.New(e.CodeTrustPolicyMissing, "invalid scope")
		}},
		{name: "authority", endpoint: "https://user@registry.example.test/v2/"},
		{name: "path", endpoint: "https://registry.example.test/v2/a/../b/manifests/x"},
		{name: "repository", endpoint: "https://registry.example.test/v2/a/child/blobs/x"},
		{name: "method", endpoint: "https://registry.example.test/v2/a/manifests/x", method: http.MethodDelete},
		{name: "token query", endpoint: "https://gitlab.example.test/jwt/auth?service=wrong&scope=repository%3Aa%3Apull"},
		{name: "token scope", endpoint: "https://gitlab.example.test/jwt/auth?service=container_registry&scope=repository%3Ab%3Apull"},
		{name: "second redirect", endpoint: "https://registry.example.test/v2/a/manifests/x", change: func(r *http.Request, _ *httpsRegistryTransport) {
			source, err := http.NewRequest(http.MethodGet, "https://storage.example.test/object", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Response = &http.Response{StatusCode: http.StatusFound, Request: source}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			body := &registryTrackedBody{Reader: strings.NewReader("synthetic body")}
			request, err := http.NewRequestWithContext(t.Context(), method, tc.endpoint, body)
			if err != nil {
				t.Fatal(err)
			}
			probe := &registryAuthorityProbe{}
			scoped := newHTTPSRegistryTransport(probe, valid)
			if tc.change != nil {
				tc.change(request, &scoped)
			}
			response, err := scoped.RoundTrip(request)
			if response != nil {
				response.Body.Close()
			}
			if (err == nil) != tc.allowed || probe.calls != boolInt(tc.allowed) || body.closed != 1 {
				t.Fatalf("allowed=%v forwarded=%d body closes=%d err=%v", tc.allowed, probe.calls, body.closed, err)
			}
			if !tc.allowed {
				want := e.CodeTrustPolicyMissing
				if tc.name == "HTTP" || tc.name == "nil URL" {
					want = e.CodeUsage
				}
				assertRegistryCode(t, err, want)
			}
		})
	}
}

type registryTrackedBody struct {
	*strings.Reader
	closed int
}

func (body *registryTrackedBody) Close() error { body.closed++; return nil }
