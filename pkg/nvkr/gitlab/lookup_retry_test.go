// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
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
	if os.Getenv("NVKR_GITLAB_RETRY_BOUND_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		// Keep cancellation/join assertions in the child. Its own timeout and
		// this outer watchdog also bound regressions in deferred cleanup.
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, executable, "-test.run=^TestGitLabRetryRespectsDeadlineAndClose$", "-test.count=1", "-test.timeout=10s")
		child.Env = append(os.Environ(), "NVKR_GITLAB_RETRY_BOUND_CHILD=1")
		child.WaitDelay = time.Second
		output, err := child.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("retry cancellation child exceeded its watchdog: %v", ctx.Err())
		}
		if err != nil {
			t.Fatalf("retry cancellation child failed: %v\n%s", err, output)
		}
		return
	}
	for _, mode := range []string{"deadline", "close"} {
		t.Run(mode, func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				// Admit the retry even within the caller's one-second budget;
				// the hook, rather than Retry-After admission, must see cancellation.
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
			})
			waiting, cancellationSeen, releaseLookup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(releaseLookup) })
			client.waitRetry = func(ctx context.Context, _ time.Duration) error {
				close(waiting)
				<-ctx.Done()
				if mode == "close" {
					close(cancellationSeen)
					<-releaseLookup
				}
				return ctx.Err()
			}
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "deadline" {
				deadlineCtx, deadlineCancel := context.WithTimeout(ctx, time.Second)
				defer deadlineCancel()
				ctx = deadlineCtx
			}
			finished := make(chan error, 1)
			var lookupResult, closeResult <-chan error = finished, nil
			// Always release both caller cancellation and the held hook before
			// joining. The enclosing child watchdog also bounds cleanup regressions.
			defer func() {
				cancel()
				release()
				if lookupResult != nil {
					<-lookupResult
				}
				if closeResult != nil {
					<-closeResult
				}
				_ = client.Close()
			}()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			go func() { _, err := client.Current(ctx, id(), []string{"nvkr:finalize"}); finished <- err }()
			// Both cases must actually enter the retry wait. A refusal before it
			// starts cannot count as evidence of deadline or lifetime propagation.
			select {
			case <-waiting:
			case err := <-finished:
				lookupResult = nil
				t.Fatalf("lookup returned before retry wait: %v", err)
			case <-deadline.C:
				t.Fatal("lookup did not enter retry wait")
			}
			if mode == "close" {
				closed := make(chan error, 1)
				closeResult = closed
				go func() { closed <- client.Close() }()
				select {
				case <-cancellationSeen:
				case err := <-closeResult:
					closeResult = nil
					t.Fatalf("Close returned before retry cancellation was observed: %v", err)
				case <-deadline.C:
					t.Fatal("Close did not cancel the active retry wait")
				}
				// Cancellation alone is insufficient: Close must still join this
				// live lookup. Keep the hook held through a bounded observation.
				hold := time.NewTimer(100 * time.Millisecond)
				defer hold.Stop()
				select {
				case err := <-closeResult:
					closeResult = nil
					t.Fatalf("Close returned while the active retry lookup was held: %v", err)
				case err := <-lookupResult:
					lookupResult = nil
					t.Fatalf("lookup returned before its retry hook was released: %v", err)
				case <-hold.C:
				}
				release()
			}
			for lookupResult != nil || closeResult != nil {
				select {
				case err := <-lookupResult:
					lookupResult = nil
					var coded *e.Error
					if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
						t.Fatalf("deadline or Close lost: %v", err)
					}
				case err := <-closeResult:
					closeResult = nil
					if err != nil {
						t.Fatal(err)
					}
				case <-deadline.C:
					t.Fatal("retry lookup and Close did not stop and join within their shared bound")
				}
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
