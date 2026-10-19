// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type admissionTransportFunc func(*http.Request) (*http.Response, error)

func (f admissionTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func admissionResponseFor(r *http.Request, status int, etag, body string) *http.Response {
	header := make(http.Header)
	if etag != "" {
		header.Set("ETag", etag)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

const admissionPrivatePackage = `{"name":"source/ci-artifacts","package_type":"container","visibility":"private","owner":{"login":"Example"}}`

func TestAdmissionRevalidatesPrivacyAndNeverUsesStaleSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		etag   string
		held   string
		body   string
		fail   bool
		cancel bool
		code   e.Code
	}{
		{name: "matching representation", status: 304, etag: `"private-v1"`},
		{name: "weak becomes strong", status: 304, held: `W/"private-v1"`, etag: `"private-v1"`},
		{name: "strong becomes weak", status: 304, etag: `W/"private-v1"`},
		{name: "different weak opaque tag", status: 304, held: `W/"private-v1"`, etag: `W/"different"`, code: e.CodeTrustPolicyMissing},
		{name: "different strong opaque tag", status: 304, held: `W/"private-v1"`, etag: `"different"`, code: e.CodeTrustPolicyMissing},
		{name: "invalid weak prefix", status: 304, etag: `w/"private-v1"`, code: e.CodeTrustPolicyMissing},
		{name: "304 omits optional validator", status: 304},
		{name: "changed public package", status: 200, etag: `"public-v2"`, body: strings.Replace(admissionPrivatePackage, `"private"`, `"public"`, 1), code: e.CodeTrustPolicyMissing},
		{name: "access revoked", status: 403, code: e.CodeTrustPolicyMissing},
		{name: "rate limited", status: 429, code: e.CodeTrustPolicyMissing},
		{name: "malformed replacement", status: 200, etag: `"bad-v2"`, body: `{`, code: e.CodeTrustPolicyMissing},
		{name: "contradictory validator", status: 304, etag: `"different"`, code: e.CodeTrustPolicyMissing},
		{name: "transport failure", fail: true, code: e.CodeFetchFailed},
		{name: "canceled request", fail: true, cancel: true, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			held := tc.held
			if held == "" {
				held = `"private-v1"`
			}
			b := newBackend()
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+testOptions.Token || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
					t.Fatal("conditional request lost authenticated API context")
				}
				if calls == 1 {
					if r.Header.Get("If-None-Match") != "" {
						t.Fatal("first request was conditional")
					}
					return admissionResponseFor(r, 200, held, admissionPrivatePackage), nil
				}
				if r.Header.Get("If-None-Match") != held {
					t.Fatal("second boundary did not revalidate the held representation")
				}
				if tc.cancel {
					cancel()
				}
				if tc.fail {
					return nil, context.DeadlineExceeded
				}
				return admissionResponseFor(r, tc.status, tc.etag, tc.body), nil
			})
			source := repositoryRecord{Owner: account{Login: "Example", Type: "Organization"}}
			if ok, err := b.checkPrivacy(ctx, testRequest, source, testOptions); err != nil || !ok {
				t.Fatalf("initial admission failed: %v", err)
			}
			ok, err := b.checkPrivacy(ctx, testRequest, source, testOptions)
			if tc.code == "" {
				if err != nil || !ok {
					t.Fatalf("revalidated package was rejected: %v", err)
				}
			} else {
				assertCode(t, err, tc.code)
				if ok {
					t.Fatal("failure reused stale private admission")
				}
			}
			if calls != 2 {
				t.Fatalf("every boundary must contact GitHub, got %d requests", calls)
			}
			if tc.code != "" && tc.status != 200 && len(b.responses.entries) != 0 {
				t.Fatal("failed response retained its old representation")
			}
		})
	}
}

func TestAdmissionRequiresHeldValidatorAndDecodesFresh(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initial  bool
		response string
		code     e.Code
	}{
		{name: "unsolicited 304", code: e.CodeTrustPolicyMissing},
		{name: "fresh decode", initial: true, response: `W/"one"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := newBackend()
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.initial && calls == 1 {
					return admissionResponseFor(r, 200, tc.response, `{"id":1,"owner":{"login":"Example"}}`), nil
				}
				return admissionResponseFor(r, 304, tc.response, ""), nil
			})
			if tc.initial {
				var first repositoryRecord
				if _, err := b.get(t.Context(), "/repos/Example/Source", "token", &first); err != nil {
					t.Fatal(err)
				}
				first.Owner.Login = "mutated caller object"
			}
			var next repositoryRecord
			status, err := b.get(t.Context(), "/repos/Example/Source", "token", &next)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil || status != 200 || next.ID != 1 || next.Owner.Login != "Example" {
				t.Fatalf("held exact bytes were not decoded anew: %+v, %d, %v", next, status, err)
			}
		})
	}
}

func TestAdmissionValidatorsAreOptionalAndBounded(t *testing.T) {
	for _, validator := range []string{"", "*", "unquoted", `"one", "two"`, "\"control\n\"", `w/"lowercase"`, `"` + strings.Repeat("x", maxAdmissionETagBytes) + `"`} {
		t.Run(fmt.Sprintf("validator length %d: %q", len(validator), validator[:min(len(validator), 30)]), func(t *testing.T) {
			calls := 0
			b := newBackend()
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				want := ""
				if calls == 2 {
					want = `"initial"`
				}
				if r.Header.Get("If-None-Match") != want {
					t.Fatal("missing/invalid replacement validator retained stale conditional state")
				}
				etag := validator
				if calls == 1 {
					etag = `"initial"`
				}
				return admissionResponseFor(r, 200, etag, `{}`), nil
			})
			for range 3 {
				var value map[string]any
				if status, err := b.get(t.Context(), "/metadata", "token", &value); status != 200 || err != nil {
					t.Fatalf("valid response without usable validator was rejected: %v", err)
				}
			}
		})
	}
}

func TestAdmissionRevalidationIsolatesCredentialAndFullURL(t *testing.T) {
	b := newBackend()
	seen := make(map[string]bool)
	b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
		key := r.URL.String() + "\n" + r.Header.Get("Authorization")
		conditional := seen[key] && r.Header.Get("Authorization") != "Bearer "
		if (r.Header.Get("If-None-Match") != "") != conditional {
			t.Fatal("representation crossed a full URL or credential boundary")
		}
		seen[key] = true
		if conditional {
			return admissionResponseFor(r, 304, `"one"`, ""), nil
		}
		return admissionResponseFor(r, 200, `"one"`, `{}`), nil
	})
	for _, step := range []struct{ api, path, token string }{
		{"https://api.github.com", "/one", "a"},
		{"https://api.github.com", "/one", "b"},
		{"https://api.github.com", "/two", "a"},
		{"https://other.example", "/one", "a"},
		{"https://api.github.com", "/one", "a"},
		{"https://api.github.com", "/one", ""},
		{"https://api.github.com", "/one", ""},
	} {
		b.api = step.api
		var value map[string]any
		if status, err := b.get(t.Context(), step.path, step.token, &value); status != 200 || err != nil {
			t.Fatalf("isolated response failed: %v", err)
		}
	}
}

func TestAdmissionResponseStorageBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"entry limit", 2},
		{"byte limit", 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cache admissionResponses
			for i := range maxAdmissionResponses + 1 {
				key := admissionResponseKey{url: fmt.Sprintf("https://api.github.com/%d", i), token: "credential"}
				cache.remember(key, admissionResponse{etag: `"one"`, raw: make([]byte, tc.size)})
			}
			if len(cache.entries) == 0 || len(cache.entries) > maxAdmissionResponses || cache.bytes > maxAdmissionResponseBytes {
				t.Fatalf("cache bounds violated: %d entries, %d bytes", len(cache.entries), cache.bytes)
			}
			overflow := admissionResponseKey{url: "https://api.github.com/overflow", token: "credential"}
			cache.remember(overflow, admissionResponse{etag: `"one"`, raw: make([]byte, tc.size)})
			if _, ok := cache.lookup(overflow); ok {
				t.Fatal("overflow representation was retained")
			}
			for key := range cache.entries {
				cache.forget(key)
			}
			if cache.bytes != 0 || len(cache.entries) != 0 {
				t.Fatal("discarding responses did not reclaim the bounded cache")
			}
		})
	}
}
