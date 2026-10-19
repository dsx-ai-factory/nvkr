// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubLocatorRateLimitAdmission(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		body             string
		header           http.Header
		attempt          int
		delay            time.Duration
		limited, failure bool
	}{
		{name: "service unavailable", status: 503, delay: 2 * time.Second, limited: true},
		{name: "server backoff", status: 502, attempt: 2, delay: 8 * time.Second, limited: true},
		{name: "service retry after", status: 503, header: http.Header{"Retry-After": {"30"}}, delay: 30 * time.Second, limited: true},
		{name: "service excessive delay", status: 503, header: http.Header{"Retry-After": {"121"}}, limited: true, failure: true},
		{name: "not implemented", status: 501},
		{name: "secondary", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, header: http.Header{"Retry-After": {"1"}}, delay: time.Second, limited: true},
		{name: "ordinary forbidden", status: 403, body: `{"message":"Resource not accessible by integration"}`, header: http.Header{"Retry-After": {"1"}}},
		{name: "duplicate message", status: 403, body: `{"message":"denied","message":"secondary rate limit"}`, header: http.Header{"Retry-After": {"1"}}},
		{name: "missing delay", status: 429, delay: 60 * time.Second, limited: true},
		{name: "increasing missing delay", status: 429, attempt: 1, delay: 120 * time.Second, limited: true},
		{name: "missing delay budget", status: 429, attempt: 2, limited: true, failure: true},
		{name: "minimum increases", status: 429, header: http.Header{"Retry-After": {"1"}}, attempt: 2, delay: 4 * time.Second, limited: true},
		{name: "excessive server delay", status: 429, header: http.Header{"Retry-After": {"121"}}, limited: true, failure: true},
		{name: "invalid delay", status: 429, header: http.Header{"Retry-After": {"SECRET"}}, limited: true, failure: true},
		{name: "ambiguous delay", status: 429, header: http.Header{"Retry-After": {"1", "2"}}, limited: true, failure: true},
		{name: "primary exhaustion", status: 429, header: http.Header{"Retry-After": {"1"}, "X-Ratelimit-Remaining": {"0"}}, limited: true, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delay, limited, err := githubLocatorRetryDelay(githubLocatorResponse{status: tc.status, header: tc.header, body: []byte(tc.body)}, tc.attempt, time.Now())
			if limited != tc.limited {
				t.Fatal("incorrect retry admission")
			}
			if tc.failure {
				assertCode(t, err, e.CodeUnavailable)
				if strings.Contains(err.Error(), "SECRET") {
					t.Fatal("raw retry header escaped")
				}
				return
			}
			if err != nil || delay != tc.delay {
				t.Fatalf("delay=%v err=%v", delay, err)
			}
		})
	}
}

func TestGitHubLocatorTransientServerRetries(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		for _, scenario := range []string{"recovers", "exhausted", "canceled"} {
			t.Run(http.StatusText(status)+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				g := newGitHubLocatorClient("test-token")
				calls := 0
				g.wait = func(context.Context, time.Duration) error {
					if scenario == "canceled" {
						cancel()
						return ctx.Err()
					}
					return nil
				}
				g.client.Transport = chunkRoundTripper(func(*http.Request) (*http.Response, error) {
					calls++
					if scenario == "recovers" && calls == 2 {
						return githubLocatorHTTPFixture(200, []byte(`{"value":7}`), nil), nil
					}
					return githubLocatorHTTPFixture(status, []byte("provider error"), nil), nil
				})
				var result struct{ Value int }
				err := g.getJSON(ctx, "/repos/org/repo/actions/runs/1", &result)
				switch scenario {
				case "recovers":
					if err != nil || calls != 2 || result.Value != 7 {
						t.Fatalf("calls=%d result=%v err=%v", calls, result, err)
					}
				case "exhausted":
					assertCode(t, err, e.CodeUnavailable)
					if calls != githubLocatorAPIAttempts {
						t.Fatal("server retries exceeded request budget")
					}
				case "canceled":
					assertCode(t, err, e.CodeTimeout)
					if calls != 1 {
						t.Fatal("server request repeated after cancellation")
					}
				}
			})
		}
}

func TestGitHubLocatorAPIRetryIsBoundedAndCancellationAware(t *testing.T) {
	for _, scenario := range []string{"recovers", "exhausted", "canceled", "ordinary forbidden", "duplicate JSON"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, waits := 0, 0
			g := newGitHubLocatorClient("test-token")
			g.wait = func(context.Context, time.Duration) error {
				waits++
				if scenario == "canceled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			g.client.Transport = chunkRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if scenario == "ordinary forbidden" {
					return githubLocatorHTTPFixture(403, []byte(`{"message":"denied"}`), nil), nil
				}
				if scenario == "duplicate JSON" {
					return githubLocatorHTTPFixture(200, []byte(`{"value":1,"value":2}`), nil), nil
				}
				if scenario == "recovers" && calls > 1 {
					return githubLocatorHTTPFixture(200, []byte(`{"value":1}`), nil), nil
				}
				return githubLocatorHTTPFixture(429, nil, http.Header{"Retry-After": {"1"}}), nil
			})
			var result struct {
				Value int `json:"value"`
			}
			err := g.getJSON(ctx, "/repos/org/repo/actions/runs/1", &result)
			switch scenario {
			case "recovers":
				if err != nil || calls != 2 || waits == 0 || result.Value != 1 {
					t.Fatalf("calls=%d waits=%d error=%v", calls, waits, err)
				}
			case "exhausted":
				assertCode(t, err, e.CodeUnavailable)
				if calls != githubLocatorAPIAttempts {
					t.Fatal("unbounded API retry")
				}
			case "canceled":
				assertCode(t, err, e.CodeTimeout)
				if calls != 1 {
					t.Fatal("API called after cancellation")
				}
			case "ordinary forbidden":
				assertCode(t, err, e.CodeFetchFailed)
				if calls != 1 || waits != 0 {
					t.Fatal("ordinary authorization denial retried")
				}
			case "duplicate JSON":
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestGitHubLocatorSignedURLAdmission(t *testing.T) {
	for _, address := range []string{"http://account.blob.core.windows.net/a", "https://account.blob.core.windows.net:443/a", "https://token@account.blob.core.windows.net/a", "https://account.blob.core.windows.net/a#fragment", "https://account.blob.core.windows.net.evil.example/a", "https://evil.example/a"} {
		_, err := githubLocatorDownloadURL(address)
		assertCode(t, err, e.CodeVerifyFailed)
	}
}

func TestGitHubLocatorHTTPDeadlineCodeDoesNotExposeProviderCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		code  e.Code
	}{
		{"client deadline", &url.Error{Op: "Get", URL: "https://SECRET.example/token", Err: context.DeadlineExceeded}, e.CodeTimeout},
		{"request canceled", context.Canceled, e.CodeTimeout},
		{"truncated body", io.ErrUnexpectedEOF, e.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := githubLocatorHTTPError(t.Context(), tc.cause, "CI locator HTTP request failed")
			assertCode(t, err, tc.code)
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("provider cause escaped")
			}
		})
	}
}
