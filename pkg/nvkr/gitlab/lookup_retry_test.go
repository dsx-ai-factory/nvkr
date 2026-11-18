// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitLabLookupThrottling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		header     []string
		persistent bool
		calls      int64
		waits      []time.Duration
		code       e.Code
	}{
		{name: "default backoff", status: 429, calls: 2, waits: []time.Duration{time.Second}},
		{name: "server delay", status: 429, header: []string{"2"}, calls: 2, waits: []time.Duration{2 * time.Second}},
		{name: "bounded exhaustion", status: 429, persistent: true, calls: 3, waits: []time.Duration{time.Second, 2 * time.Second}, code: e.CodeUnavailable},
		{name: "authentication is not retried", status: 401, calls: 1, code: e.CodeUnavailable},
		{name: "other failures are not retried", status: 500, calls: 1, code: e.CodeUnavailable},
		{name: "invalid delay", status: 429, header: []string{"PRIVATE_SENTINEL"}, calls: 1, code: e.CodeUnavailable},
		{name: "ambiguous delay", status: 429, header: []string{"1", "2"}, calls: 1, code: e.CodeUnavailable},
		{name: "excessive delay", status: 429, header: []string{"31"}, calls: 1, code: e.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v4/job" || r.Header.Get("JOB-TOKEN") != "job-secret" || r.Header.Get("PRIVATE-TOKEN") != "" {
					t.Error("retry changed request authority or credentials")
				}
				if calls.Add(1) == 1 || tc.persistent {
					if tc.header != nil {
						w.Header()["Retry-After"] = tc.header
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"private":"PRIVATE_SENTINEL"}`))
					return
				}
				_, _ = w.Write([]byte(`{"accepted":true}`))
			})
			defer client.Close()
			var waits []time.Duration
			client.waitRetry = func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay)
				return nil
			}
			var result struct{ Accepted bool }
			err := client.get(t.Context(), "/job", "job-token", "job-secret", &result)
			if tc.code == "" {
				if err != nil || !result.Accepted {
					t.Fatalf("throttled read did not recover: %v", err)
				}
			} else {
				if e.ExitCode(err) != e.ExitCode(e.New(tc.code, "expected")) || result.Accepted {
					t.Fatalf("unexpected refusal or accepted response: %v", err)
				}
				if strings.Contains(err.Error(), "PRIVATE_SENTINEL") || strings.Contains(err.Error(), "job-secret") {
					t.Fatal("retry disclosed provider details or credentials")
				}
			}
			if calls.Load() != tc.calls || !slices.Equal(waits, tc.waits) {
				t.Fatalf("calls=%d waits=%v; want %d %v", calls.Load(), waits, tc.calls, tc.waits)
			}
		})
	}
}

func TestGitLabRetryDelay(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
		bad   bool
	}{
		{name: "seconds", value: "30", want: 30 * time.Second},
		{name: "zero", value: "0"},
		{name: "HTTP date", value: now.Add(5 * time.Second).Format(http.TimeFormat), want: 5 * time.Second},
		{name: "past date", value: now.Add(-time.Second).Format(http.TimeFormat)},
		{name: "excessive date", value: now.Add(time.Minute).Format(http.TimeFormat), bad: true},
		{name: "integer overflow", value: "99999999999999999999", bad: true},
		{name: "negative", value: "-1", bad: true},
		{name: "empty", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitlabRetryDelay(http.Header{"Retry-After": {tc.value}}, 0, now)
			if tc.bad {
				if e.ExitCode(err) != e.ExitCode(e.New(e.CodeUnavailable, "expected")) {
					t.Fatalf("invalid delay accepted: %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("delay=%v error=%v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestGitLabRetryRespectsDeadlineAndClose(t *testing.T) {
	for _, mode := range []string{"deadline", "close"} {
		t.Run(mode, func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			})
			defer client.Close()
			waiting := make(chan struct{})
			client.waitRetry = func(ctx context.Context, _ time.Duration) error {
				close(waiting)
				<-ctx.Done()
				return ctx.Err()
			}
			ctx := t.Context()
			if mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			finished := make(chan error, 1)
			go func() { _, err := client.Current(ctx, id(), []string{"nvkr:finalize"}); finished <- err }()
			if mode == "close" {
				select {
				case <-waiting:
				case err := <-finished:
					t.Fatalf("lookup returned before retry wait: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("lookup did not enter retry wait")
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-finished:
				if e.ExitCode(err) != e.ExitCode(e.New(e.CodeTimeout, "expected")) {
					t.Fatalf("deadline or Close lost: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("retry did not stop and join")
			}
		})
	}
}

func TestGitLabRetryWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitGitLabRetry(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("wait ignored cancellation: %v", err)
	}
}
