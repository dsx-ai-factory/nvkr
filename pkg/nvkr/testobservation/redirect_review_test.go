// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
)

func TestObservationExactBlobHTTPSRedirect(t *testing.T) {
	raw := []byte("exact covered result")
	for _, tc := range []struct {
		name  string
		media string
		bad   bool
		want  e.Code
	}{
		{"covered blob", FileMediaType, false, ""},
		{"covered configuration", ObservationMediaType, false, ""},
		{"tampered content", FileMediaType, true, e.CodeVerifyFailed},
		{"manifest cannot redirect", oci.MediaTypeImageManifest, false, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contacted := 0
			storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contacted++
				for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Cookie2"} {
					if r.Header.Get(h) != "" {
						t.Fatalf("credential reached storage via %s", h)
					}
				}
				if r.Method != http.MethodGet {
					t.Fatal("non-GET delegated")
				}
				body := raw
				if tc.bad {
					body = []byte("wrong covered result")
				}
				_, _ = w.Write(body)
			}))
			defer storage.Close()
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u, p, ok := r.BasicAuth()
				if !ok {
					w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if u != "fixture" || p != "secret" {
					t.Fatal("wrong origin credential")
				}
				http.Redirect(w, r, storage.URL+"/storage", http.StatusTemporaryRedirect)
			}))
			defer registry.Close()
			repo, err := observationRepository(strings.TrimPrefix(registry.URL, "http://")+"/observations", delivery.RegistryOptions{PlainHTTP: true, Username: "fixture", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			client := repo.Client.(*auth.Client).Client
			original := client.Transport.(registryTransport)
			original.base = storage.Client().Transport
			client.Transport = original
			desc := descriptor(tc.media, raw)
			got, err := fetchExact(t.Context(), repo, desc, 1024)
			if tc.want != "" {
				requireCode(t, err, tc.want)
				if got != nil {
					t.Fatal("unverified bytes escaped")
				}
			} else if err != nil || string(got) != string(raw) {
				t.Fatalf("blob redirect: %q %v", got, err)
			}
			if tc.media == oci.MediaTypeImageManifest && contacted != 0 {
				t.Fatal("manifest reached storage")
			}
		})
	}
}

func TestObservationBlobDelegationDoesNotAuthorizeOtherRequests(t *testing.T) {
	for _, tc := range []struct {
		name, url, method string
		blob              bool
	}{
		{"HTTPS blob", "https://storage.example/blob", http.MethodGet, true},
		{"plaintext", "http://storage.example/blob", http.MethodGet, true},
		{"upload", "https://storage.example/blob", http.MethodPost, true},
		{"manifest", "https://storage.example/blob", http.MethodGet, false},
		{"user info", "https://user:secret@storage.example/blob", http.MethodGet, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.blob {
				ctx = context.WithValue(ctx, blobFetchKey{}, blobFetchPermission{path: "/v2/repo/blobs/" + rawPin("x")})
			}
			original, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.example/v2/repo/blobs/"+rawPin("x"), nil)
			req, _ := http.NewRequestWithContext(ctx, tc.method, tc.url, nil)
			req.Header.Set("Authorization", "secret")
			req.Header.Set("Cookie", "secret")
			err := checkObservationRedirect(req, []*http.Request{original}, "https", "registry.example")
			if tc.name == "HTTPS blob" {
				if err != nil {
					t.Fatal(err)
				}
				if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
					t.Fatal("redirect retained credential")
				}
			} else {
				requireCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
	// A token challenge uses the parent fetch context, never the private marker
	// granted to the HTTP client's validated redirect request.
	ctx := context.WithValue(t.Context(), blobFetchKey{}, blobFetchPermission{path: "/v2/repo/blobs/" + rawPin("x")})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://storage.example/token", nil)
	response, err := (registryTransport{base: http.DefaultTransport, scheme: "https", host: "registry.example"}).RoundTrip(req)
	if response != nil {
		_ = response.Body.Close()
	}
	requireCode(t, err, e.CodeVerifyFailed)
}
