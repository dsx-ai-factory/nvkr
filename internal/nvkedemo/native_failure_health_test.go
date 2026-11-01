// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeFailureHTTPStatusIsBoundedAndBodyFree(t *testing.T) {
	for _, status := range []int{200, 401, 503, 700} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("probe sent unexpected request or credentials")
				}
				w.Header().Set("X-Private", "PRIVATE_HEADER")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("PRIVATE_DEPENDENCY_ERROR"))
			}))
			defer server.Close()
			want := status
			if status > 599 {
				want = 0
			}
			if got := nativeFailureHTTPStatus(t.Context(), server.URL+"/readyz"); got != want {
				t.Fatalf("status=%d want=%d", got, want)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Private", strings.Repeat("x", 70<<10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if got := nativeFailureHTTPStatus(t.Context(), server.URL+"/healthz"); got != 0 {
		t.Fatal("oversized headers escaped the bounded observation")
	}
}

func TestNativeFailureHTTPStatusNeverFollowsRedirectsOrCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, target.URL+"/PRIVATE_TARGET", http.StatusFound)
	}))
	defer server.Close()
	if status := nativeFailureHTTPStatus(t.Context(), server.URL+"/healthz"); status != http.StatusFound || targetCalls.Load() != 0 {
		t.Fatal("redirect was not retained as a status-only observation")
	}
	for _, endpoint := range []string{
		strings.Replace(server.URL, "http://", "http://PRIVATE_USER:PRIVATE_PASSWORD@", 1) + "/healthz",
		server.URL + "/readyz?PRIVATE_TOKEN=secret",
		server.URL + "/readyz#PRIVATE_FRAGMENT",
		server.URL + "/api/clusters",
	} {
		if status := nativeFailureHTTPStatus(t.Context(), endpoint); status != 0 {
			t.Fatal("unsupported or credential-bearing diagnostic was admitted")
		}
	}
	if calls.Load() != 1 || targetCalls.Load() != 0 {
		t.Fatal("refused inputs reached an HTTP target")
	}
}

func TestNativeFailureHTTPStatusCancellationClosesRequest(t *testing.T) {
	entered, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(closed)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	joined := make(chan int, 1)
	done := make(chan struct{})
	go func() { defer close(done); joined <- nativeFailureHTTPStatus(ctx, server.URL+"/readyz") }()
	t.Cleanup(func() {
		cancel()
		server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("probe worker did not join during cleanup")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case status := <-joined:
		if status != 0 {
			t.Fatal("canceled request became an HTTP status")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled probe did not join")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled probe retained its connection")
	}
}

func TestNativeFailureHTTPStatusClosesUnreadBody(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if status := nativeFailureHTTPStatus(ctx, server.URL+"/readyz"); status != http.StatusServiceUnavailable {
		t.Fatal("probe waited for or consumed an unfinished private body")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("status-only probe did not close its unread response")
	}
}

func TestNativeSiteHealthRejectsUnobservedAddresses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	loopback, err := netip.ParseAddrPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []netip.AddrPort{{}, loopback, netip.MustParseAddrPort("192.0.2.1:30080"), netip.MustParseAddrPort("[fd00::1]:30080")} {
		var progress bootstrapObservationRecorder
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		observeNativeSiteHealth(ctx, Runner{Progress: &progress}, endpoint)
		cancel()
		want := "Site REST listener observation (not acceptance): unavailable.\nSite REST dependencies observation (not acceptance): unavailable.\n"
		if progress.String() != want {
			t.Fatal("unadmitted address altered the fixed observation")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unadmitted address reached a listener")
	}
}
