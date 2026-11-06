// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const admissionSource = `{"id":1,"full_name":"Example/Source","owner":{"login":"Example","type":"Organization"}}`

func TestAdmissionSourceRetriesAvailabilityWithoutWeakeningIdentity(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		header     http.Header
		persistent bool
		calls      int
		waits      []time.Duration
		code       e.Code
	}{
		{name: "500 then success", status: 500, calls: 2, waits: []time.Duration{2 * time.Second}},
		{name: "other 5xx then success", status: 501, calls: 2, waits: []time.Duration{2 * time.Second}},
		{name: "500 exhausted primary quota", status: 500, header: http.Header{"X-Ratelimit-Remaining": {"0"}}, calls: 1, code: e.CodeUnavailable},
		{name: "500 ambiguous primary quota", status: 500, header: http.Header{"X-Ratelimit-Remaining": {"1", "1"}}, calls: 1, code: e.CodeUnavailable},
		{name: "501 exhausted primary quota", status: 501, header: http.Header{"X-Ratelimit-Remaining": {"0"}}, calls: 1, code: e.CodeUnavailable},
		{name: "501 ambiguous primary quota", status: 501, header: http.Header{"X-Ratelimit-Remaining": {"1", "1"}}, calls: 1, code: e.CodeUnavailable},
		{name: "persistent 502", status: 502, persistent: true, calls: 4, waits: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, code: e.CodeUnavailable},
		{name: "429 Retry-After seconds", status: 429, header: http.Header{"Retry-After": {"7"}}, calls: 2, waits: []time.Duration{7 * time.Second}},
		{name: "429 Retry-After date", status: 429, header: http.Header{"Retry-After": {now.Add(45 * time.Second).Format(http.TimeFormat)}}, calls: 2, waits: []time.Duration{45 * time.Second}},
		{name: "503 Retry-After", status: 503, header: http.Header{"Retry-After": {"30"}}, calls: 2, waits: []time.Duration{30 * time.Second}},
		{name: "secondary limit", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, header: http.Header{"Retry-After": {"3"}}, calls: 2, waits: []time.Duration{3 * time.Second}},
		{name: "secondary fallback", status: 403, body: `{"message":"secondary rate limit"}`, calls: 2, waits: []time.Duration{time.Minute}},
		{name: "ordinary forbidden", status: 403, body: `{"message":"denied"}`, header: http.Header{"Retry-After": {"1"}}, calls: 1, code: e.CodeFetchFailed},
		{name: "ambiguous forbidden", status: 403, body: `{"message":"denied","message":"secondary rate limit"}`, calls: 1, code: e.CodeFetchFailed},
		{name: "unauthorized", status: 401, calls: 1, code: e.CodeFetchFailed},
		{name: "not found", status: 404, calls: 1, code: e.CodeFetchFailed},
		{name: "invalid delay", status: 429, header: http.Header{"Retry-After": {"credential-marker"}}, calls: 1, code: e.CodeUnavailable},
		{name: "excessive delay", status: 503, header: http.Header{"Retry-After": {"121"}}, calls: 1, code: e.CodeUnavailable},
		{name: "ambiguous delay", status: 429, header: http.Header{"Retry-After": {"1", "2"}}, calls: 1, code: e.CodeUnavailable},
		{name: "wrong owner", status: 200, body: strings.Replace(admissionSource, `"login":"Example"`, `"login":"Other"`, 1), calls: 1, code: e.CodeTrustPolicyMissing},
		{name: "missing ID", status: 200, body: strings.Replace(admissionSource, `"id":1`, `"id":0`, 1), calls: 1, code: e.CodeTrustPolicyMissing},
		{name: "wrong name", status: 200, body: strings.Replace(admissionSource, `"Example/Source"`, `"Example/Other"`, 1), calls: 1, code: e.CodeTrustPolicyMissing},
		{name: "wrong type", status: 200, body: strings.Replace(admissionSource, `"Organization"`, `"Bot"`, 1), calls: 1, code: e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend()
			b.now = func() time.Time { return now }
			var waits []time.Duration
			calls, closes := 0, 0
			b.wait = func(_ context.Context, delay time.Duration) error {
				if closes != calls {
					t.Fatal("retry wait started with an open response")
				}
				waits = append(waits, delay)
				return nil
			}
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.github.com/repos/Example/Source" || r.Header.Get("Authorization") != "Bearer repository-only" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
					t.Fatal("retry changed endpoint or credential scope")
				}
				status, body := tc.status, tc.body
				if calls > 1 && !tc.persistent {
					status, body = 200, admissionSource
				}
				response := admissionResponseFor(r, status, "", body)
				for key, values := range tc.header {
					response.Header[key] = values
				}
				response.Body = &chunkRetryCloser{Reader: response.Body, close: func() error { closes++; return nil }}
				return response, nil
			})
			record, err := b.source(t.Context(), "Example/Source", Options{Token: "package-only", RepositoryToken: "repository-only"})
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if tc.status != 200 && !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
					t.Fatalf("failure omitted provider status: %v", err)
				}
				if strings.Contains(err.Error(), "credential-marker") {
					t.Fatal("provider data leaked into error")
				}
			} else if err != nil || record.ID != 1 {
				t.Fatalf("valid repository was not admitted after retry: %v", err)
			}
			if calls != tc.calls || closes != calls || !reflect.DeepEqual(waits, tc.waits) {
				t.Fatalf("calls=%d closes=%d waits=%v; want calls=%d waits=%v", calls, closes, waits, tc.calls, tc.waits)
			}
		})
	}
}

func TestAdmissionRetryOperationBudget(t *testing.T) {
	for _, endpoint := range []string{"source", "package"} {
		for _, tc := range []struct {
			name    string
			parent  time.Duration
			budget  time.Duration
			calls   int
			elapsed time.Duration
			code    e.Code
		}{
			{"retry beyond former cap", time.Hour, 8 * time.Minute, 2, time.Minute, ""},
			{"earlier parent deadline", 45 * time.Second, 45 * time.Second, 1, 45 * time.Second, e.CodeTimeout},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := time.Now()
					ctx, cancel := context.WithTimeout(t.Context(), tc.parent)
					defer cancel()
					b := newBackend()
					calls, waits := 0, 0
					b.wait = func(ctx context.Context, delay time.Duration) error {
						waits++
						deadline, ok := ctx.Deadline()
						if !ok || !deadline.Equal(started.Add(tc.budget)) {
							t.Fatalf("admission deadline=%v; want %v", deadline, started.Add(tc.budget))
						}
						return waitChunkRetry(ctx, delay)
					}
					b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						deadline, ok := r.Context().Deadline()
						if !ok || !deadline.Equal(time.Now().Add(30*time.Second)) {
							t.Fatalf("individual request deadline=%v; want 30 seconds from now", deadline)
						}
						if calls == 1 {
							response := admissionResponseFor(r, 503, "", "")
							response.Header.Set("Retry-After", "60")
							return response, nil
						}
						body := admissionSource
						if endpoint == "package" {
							body = admissionPrivatePackage
						}
						return admissionResponseFor(r, 200, "", body), nil
					})
					var err error
					if endpoint == "source" {
						_, err = b.source(ctx, testRequest.Repository, testOptions)
					} else {
						source := repositoryRecord{Owner: account{Login: "Example", Type: "Organization"}}
						_, err = b.checkPrivacy(ctx, testRequest, source, testOptions)
					}
					if tc.code != "" {
						assertCode(t, err, tc.code)
					} else if err != nil {
						t.Fatal(err)
					}
					if calls != tc.calls || waits != 1 || time.Since(started) != tc.elapsed {
						t.Fatalf("calls=%d waits=%d elapsed=%v; want calls=%d waits=1 elapsed=%v", calls, waits, time.Since(started), tc.calls, tc.elapsed)
					}
				})
			})
		}
	}
}

func TestAdmissionPackageRetryDiscardsFailedConditionalRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   e.Code
		exists bool
	}{
		{"fresh private package", 200, admissionPrivatePackage, "", true},
		{"fresh public package", 200, strings.Replace(admissionPrivatePackage, `"private"`, `"public"`, 1), e.CodeTrustPolicyMissing, false},
		{"stale 304", 304, "", e.CodeTrustPolicyMissing, false},
		{"missing package", 404, "", "", false},
		{"persistent 502", 502, "", e.CodeUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend()
			calls := 0
			b.wait = func(context.Context, time.Duration) error {
				if len(b.responses.entries) != 0 {
					t.Fatal("failed response retained cached admission")
				}
				return nil
			}
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.EscapedPath() != "/orgs/Example/packages/container/source%2Fci-artifacts" || r.Header.Get("Authorization") != "Bearer package-only" {
					t.Fatal("package retry crossed endpoint or credential boundary")
				}
				wantETag := ""
				if calls == 2 {
					wantETag = `"private-v1"`
				}
				if r.Header.Get("If-None-Match") != wantETag {
					t.Fatal("retry reused failed conditional representation")
				}
				switch calls {
				case 1:
					return admissionResponseFor(r, 200, `"private-v1"`, admissionPrivatePackage), nil
				case 2:
					return admissionResponseFor(r, 500, "", ""), nil
				default:
					return admissionResponseFor(r, tc.status, `"private-v1"`, tc.body), nil
				}
			})
			source := repositoryRecord{Owner: account{Login: "Example", Type: "Organization"}}
			opts := Options{Token: "package-only", RepositoryToken: "repository-only"}
			if _, err := b.checkPrivacy(t.Context(), testRequest, source, opts); err != nil {
				t.Fatal(err)
			}
			exists, err := b.checkPrivacy(t.Context(), testRequest, source, opts)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			wantCalls := 3
			if tc.status == 502 {
				wantCalls = 5
			}
			if exists != tc.exists || calls != wantCalls {
				t.Fatalf("exists=%v calls=%d; want exists=%v calls=%d", exists, calls, tc.exists, wantCalls)
			}
		})
	}
}

func TestAdmissionRetryCancellationStopsRequests(t *testing.T) {
	for _, duringWait := range []bool{false, true} {
		t.Run(fmt.Sprintf("during wait %v", duringWait), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b := newBackend()
			calls := 0
			b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return admissionResponseFor(r, 500, "", ""), nil
			})
			b.wait = func(ctx context.Context, delay time.Duration) error {
				cancel()
				return waitChunkRetry(ctx, delay)
			}
			if !duringWait {
				cancel()
			}
			_, err := b.source(ctx, "Example/Source", testOptions)
			assertCode(t, err, e.CodeTimeout)
			wantCalls := 0
			if duringWait {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("cancellation allowed extra requests: %d", calls)
			}
		})
	}
}
