// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"net/http"
	"strings"
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

func (p *registryAuthorityProbe) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls++
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
			response, err := (httpsRegistryTransport{base: p, authority: scope}).RoundTrip(request)
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
	response, err := (httpsRegistryTransport{base: probe, authority: scope}).RoundTrip(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || probe.calls != 0 {
		t.Fatal("delete request reached registry transport")
	}
}
