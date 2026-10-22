// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Exercise the native callback's real finalizer. A returned/panicked operation
// must not invalidate its native scope until the live owner admits cleanup.
func TestNativeScopeRetainsUntilExplicitCleanup(t *testing.T) {
	for _, mode := range []string{"ready", "failure", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, cancelWork := retainedTestOwner(t)
			var scope *nativeSiteScope
			joined := make(chan error, 1)
			go func() {
				joined <- withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", func(ctx context.Context, site *NativeSite) error {
					scope = site.scope
					switch mode {
					case "ready":
						return owner.markReady(ctx, OperatorAccess{kubectl: "/fixture/tool", site: "/fixture/site", tenant: "/fixture/tenant", siteQCOW: true})
					case "failure":
						return errors.New("synthetic suite failure")
					case "cancel":
						cancelWork()
						return ctx.Err()
					default:
						panic("synthetic operation panic")
					}
				})
			}()
			state := retainedObserve(t, owner)
			if (state.state == retainedReady) != (mode == "ready") {
				t.Fatal("failed operation invented readiness", state.state)
			}
			// Canceling work after success also grants no cleanup authority.
			cancelWork()
			select {
			case err := <-joined:
				t.Fatal("native scope unwound without explicit cleanup", err)
			default:
			}
			if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-joined:
				if (err == nil) != (mode == "ready") || scope == nil || !scope.closed {
					t.Fatal("cleanup lost scope invalidation or original failure", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("explicit native cleanup did not join")
			}
		})
	}
}

func TestNativePanicCleanupRequiresExplicitRequestAndJoinedScope(t *testing.T) {
	owner, work, _ := retainedTestOwner(t)
	root := privateTestDir(t)
	privatePath, deleted := filepath.Join(root, "owner-panic.log"), filepath.Join(root, "deleted")
	owner.panicRecorder = func(raw []byte) error { return privateWrite(privatePath, raw) }
	writeTenantFixture(t, filepath.Join(root, "kind"), []byte("#!/bin/sh\ntouch \"$DELETED\"\n"), 0o700)
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "DELETED=" + deleted}}
	attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root}, bootstrapRunner: &runner, bootstrapName: "owned"}
	control := &retainedControl{owner: owner, finish: make(chan struct{}),
		description: retainedControlDescription{Work: "/owned-work", Selector: "/owned-session", Token: "fixture-token"}}
	joined, done := make(chan error, 1), make(chan struct{})
	var scope *nativeSiteScope
	go func() {
		defer close(done)
		result := withNativeSiteScope(work, &attempt, runner, root, root, func(_ context.Context, site *NativeSite) error {
			scope = site.scope
			panic("PRIVATE-NATIVE-PANIC")
		})
		cleanup, stop := context.WithTimeout(context.WithoutCancel(work), 5*time.Second)
		defer stop()
		cleanupErr := attempt.close(cleanup)
		recordRetainedCleanup(work, cleanupErr)
		joined <- errors.Join(result, cleanupErr)
	}()
	defer func() {
		_ = owner.requestCleanup(context.WithoutCancel(t.Context()))
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("native panic owner did not join")
		}
	}()
	if snapshot := retainedObserve(t, owner); snapshot.state != retainedPartial {
		t.Fatal("native panic did not hold before teardown", snapshot)
	}
	private, err := os.ReadFile(privatePath)
	state, stateErr := control.snapshot(t.Context())
	if err != nil || !bytes.Contains(private, []byte("PRIVATE-NATIVE-PANIC")) || stateErr != nil || state.FailureCode != nvkrerr.CodeInternal || state.TeardownReturned {
		t.Fatal("panic diagnostics unavailable before explicit cleanup", err, state, stateErr)
	}
	if _, err := os.Stat(deleted); !os.IsNotExist(err) {
		t.Fatal("held native scope deleted its bootstrap")
	}
	request := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), retainedRequesterKey{}, true))
		req.Header.Set("Authorization", "Bearer "+control.description.Token)
		req.Header.Set("X-NVKE-Retained-Work", control.description.Work)
		req.Header.Set("X-NVKE-Retained-Session", control.description.Selector)
		response := httptest.NewRecorder()
		control.handle(response, req)
		return response.Code
	}
	if request("/finish") == http.StatusOK || request("/cleanup") != http.StatusOK {
		t.Fatal("finish bypassed joined cleanup or explicit cleanup was refused")
	}
	select {
	case result := <-joined:
		var coded *nvkrerr.Error
		if !errors.As(result, &coded) || coded.Code != nvkrerr.CodeInternal || errors.Is(result, ErrRetainedControls) || scope == nil || !scope.closed || attempt.bootstrapRunner != nil {
			t.Fatal("native cleanup lost failure, scope invalidation or known bootstrap cleanup", result)
		}
		if _, err := os.Stat(deleted); err != nil {
			t.Fatal("actual native cleanup did not delete its bootstrap", err)
		}
		owner.recordTeardownReturn(result)
		if err := control.recordJoined(t.Context(), nil); err != nil || request("/finish") != http.StatusOK {
			t.Fatal("actual joined native cleanup did not permit owner finish", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("explicit cleanup did not join the native panic owner")
	}
}

func TestNativeScopeConcurrentCleanupCancelsExactlyOnce(t *testing.T) {
	work, cancelWork := context.WithCancel(t.Context())
	var cancellations atomic.Int32
	owner, err := newRetainedRuntime(context.WithoutCancel(work), func() { cancellations.Add(1); cancelWork() })
	if err != nil {
		t.Fatal(err)
	}
	work = context.WithValue(work, retainedRuntimeKey{}, owner)
	joined, done := make(chan error, 1), make(chan struct{})
	var scope *nativeSiteScope
	go func() {
		defer close(done)
		joined <- withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", func(_ context.Context, site *NativeSite) error {
			scope = site.scope
			return nil
		})
	}()
	defer func() {
		_ = owner.requestCleanup(context.WithoutCancel(t.Context()))
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("concurrent cleanup did not join the native scope")
		}
	}()
	if snapshot := retainedObserve(t, owner); snapshot.state != retainedPartial {
		t.Fatal("scope did not hold before explicit cleanup", snapshot)
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if err := owner.requestCleanup(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	select {
	case err := <-joined:
		if err != nil || scope == nil || !scope.closed || cancellations.Load() != 1 {
			t.Fatal("concurrent cleanup repeated cancellation or lost scope invalidation", err, cancellations.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit cleanup did not join")
	}
}
