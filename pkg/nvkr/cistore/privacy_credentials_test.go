// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrivateCarrierSeparatesRepositoryAndPackageCredentials(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlinked", true: "linked private"}[linked], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var response any
				want := "Bearer package-read-only"
				switch r.URL.EscapedPath() {
				case "/repos/Example/Source":
					want = "Bearer repository-read-only"
					response = map[string]any{"id": 1, "full_name": "Example/Source", "owner": map[string]any{"login": "Example", "type": "Organization"}}
				case "/orgs/Example/packages/container/source%2Fci-artifacts":
					p := privatePackage()
					if linked {
						p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": "private"}
					}
					response = p
				default:
					t.Errorf("unexpected admission endpoint: %s", r.URL.EscapedPath())
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") != want {
					t.Error("credential crossed its repository/package boundary")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			b := &backend{api: server.URL, client: server.Client()}
			opts := Options{Token: "package-read-only", RepositoryToken: "repository-read-only"}
			source, err := b.source(t.Context(), testRequest.Repository, opts)
			if err != nil {
				t.Fatal(err)
			}
			exists, err := b.checkPrivacy(t.Context(), testRequest, source, opts)
			if linked {
				assertCode(t, err, e.CodeTrustPolicyMissing)
			} else if err != nil || !exists {
				t.Fatalf("separate read credentials failed admission: %v", err)
			}
		})
	}
}

func TestPrivateCarrierRejectsMalformedRepositoryCredential(t *testing.T) {
	_, _, _, err := admit(t.Context(), testRequest, Options{Token: "package-token", RepositoryToken: "invalid\nheader"})
	assertCode(t, err, e.CodeTrustPolicyMissing)
}

func TestPackageAssociationMatchesGitHubOptionalNullableSchema(t *testing.T) {
	for _, tc := range []struct {
		name        string
		edit        func(map[string]any)
		transfer    bool
		publication bool
	}{
		{"omitted", func(map[string]any) {}, true, true},
		{"null", func(p map[string]any) { p["repository"] = nil }, true, true},
		{"private association", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": "private"}
		}, false, true},
		{"private association omitted visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true}
		}, false, true},
		{"private association unknown visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": "unknown"}
		}, false, false},
		{"private association empty visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": ""}
		}, false, false},
		{"private association null visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": nil}
		}, false, false},
		{"private association contradictory visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Private", "private": true, "visibility": "public"}
		}, false, false},
		{"association missing private", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Unknown", "visibility": "private"}
		}, false, false},
		{"association false private omitted visibility", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Public", "private": false}
		}, false, false},
		{"public association", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Public", "private": false, "visibility": "public"}
		}, false, false},
		{"unknown object", func(p map[string]any) { p["repository"] = map[string]any{} }, false, false},
		{"array", func(p map[string]any) { p["repository"] = []any{} }, false, false},
		{"string", func(p map[string]any) { p["repository"] = "null" }, false, false},
		{"missing owner", func(p map[string]any) { delete(p, "owner") }, false, false},
		{"missing type", func(p map[string]any) { delete(p, "package_type") }, false, false},
		{"missing name", func(p map[string]any) { delete(p, "name") }, false, false},
		{"missing visibility", func(p map[string]any) { delete(p, "visibility") }, false, false},
		{"public omitted association", func(p map[string]any) { p["visibility"] = "public" }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStorageFixture(t, func(int) (int, any) {
				p := privatePackage()
				tc.edit(p)
				return http.StatusOK, p
			})
			source, err := f.b.source(t.Context(), testRequest.Repository, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []struct {
				requireUnlinked bool
				admitted        bool
			}{{true, tc.transfer}, {false, tc.publication}} {
				exists, err := f.b.checkPackagePrivacy(t.Context(), "source/ci-artifacts", source, testOptions, mode.requireUnlinked)
				if mode.admitted {
					if err != nil || !exists {
						t.Fatalf("documented package representation was rejected: %v", err)
					}
				} else {
					assertCode(t, err, e.CodeTrustPolicyMissing)
					if exists {
						t.Fatal("invalid package was admitted")
					}
				}
			}
			if f.transport.calls.Load() != 0 {
				t.Fatal("package metadata admission contacted payload storage")
			}
		})
	}
}
