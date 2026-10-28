// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func retainedTestOwner(t *testing.T) (*retainedRuntime, context.Context, context.CancelFunc) {
	t.Helper()
	work, cancelWork := context.WithCancel(t.Context())
	owner, err := newRetainedRuntime(context.WithoutCancel(work), cancelWork)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return owner, context.WithValue(work, retainedRuntimeKey{}, owner), cancelWork
}

func retainedObserve(t *testing.T, owner *retainedRuntime) retainedRuntimeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		owner.mu.Lock()
		snapshot := owner.snapshot
		owner.mu.Unlock()
		if snapshot.state != retainedStarting {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture owner did not reach its hold", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestRetainedPanicIsRecordedBeforeNestedHold(t *testing.T) {
	for _, kind := range []string{"panic", "timeout", "raw deadline", "wrapped deadline"} {
		t.Run(kind, func(t *testing.T) {
			owner, work, _ := retainedTestOwner(t)
			privatePath := filepath.Join(privateTestDir(t), "owner-panic.log")
			owner.panicRecorder = func(raw []byte) error { return privateWrite(privatePath, raw) }
			joined := make(chan error, 1)
			done := make(chan struct{})
			defer func() {
				_ = owner.requestCleanup(context.WithoutCancel(t.Context()))
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("panic fixture did not join")
				}
			}()
			go func() {
				defer close(done)
				result := withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", func(context.Context, *NativeSite) error {
					if kind == "panic" {
						panic("PRIVATE-PANIC-PAYLOAD")
					}
					if kind == "raw deadline" {
						return context.DeadlineExceeded
					}
					if kind == "wrapped deadline" {
						return &CommandError{Phase: "private-command", ExitCode: -1, Cause: context.DeadlineExceeded}
					}
					return nvkrerr.Wrap(nvkrerr.CodeTimeout, context.DeadlineExceeded, "private-timeout-cause")
				})
				joined <- result
			}()
			if state := retainedObserve(t, owner); state.state != retainedPartial {
				t.Fatal(state)
			}
			control := &retainedControl{owner: owner}
			state, err := control.snapshot(t.Context())
			want := nvkrerr.CodeTimeout
			if kind == "panic" {
				want = nvkrerr.CodeInternal
				private, readErr := os.ReadFile(privatePath)
				info, statErr := os.Stat(privatePath)
				if readErr != nil || statErr != nil || info.Mode().Perm() != 0o600 || info.Size() > (1<<20)+(33<<10) || !bytes.Contains(private, []byte("recoverRetainedPanic")) || !bytes.Contains(private, []byte("PRIVATE-PANIC-PAYLOAD")) {
					t.Fatal("private panic value or stack hidden until cleanup")
				}
			}
			if err != nil || state.FailureCode != want || state.TeardownReturned {
				t.Fatal("public failure classification unavailable before hold", state, err)
			}
			raw, _ := json.Marshal(state)
			if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte("private-timeout")) {
				t.Fatal("private cause crossed control")
			}
			if err := owner.requestCleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-joined:
				if kind == "raw deadline" || kind == "wrapped deadline" {
					if !errors.Is(result, context.DeadlineExceeded) {
						t.Fatal("raw owner deadline identity lost", result)
					}
					break
				}
				var coded *nvkrerr.Error
				if !errors.As(result, &coded) || coded.Code != want {
					t.Fatal("owner primary lost", result)
				}
				if bytes.Contains([]byte(result.Error()), []byte("PRIVATE-PANIC-PAYLOAD")) {
					t.Fatal("private panic value crossed public error")
				}
			case <-time.After(time.Second):
				t.Fatal("fixture stack not joined")
			}
		})
	}
}

func TestRetainedRuntimeOperationCancellationNeverAuthorizesCleanup(t *testing.T) {
	for _, mode := range []string{"work canceled", "deadline expired", "viewer gone", "returned failure", "ready"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, cancelWork := retainedTestOwner(t)
			if mode == "ready" {
				if err := owner.markReady(work, OperatorAccess{kubectl: "/tool", site: "/site", tenant: "/tenant"}); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "deadline expired":
				deadline, cancel := context.WithDeadline(work, time.Now().Add(-time.Second))
				defer cancel()
				work = deadline
			case "work canceled", "viewer gone":
				cancelWork()
			}
			var cleaned atomic.Bool
			joined := make(chan error, 1)
			go func() {
				err := waitRetainedCleanup(work)
				if err == nil {
					cleaned.Store(true)
				}
				joined <- err
			}()
			snapshot := retainedObserve(t, owner)
			want := retainedPartial
			if mode == "ready" {
				want = retainedReady
			}
			if snapshot.state != want || cleaned.Load() || want == retainedPartial && (!snapshot.checkedAt.IsZero() || snapshot.access != (OperatorAccess{})) {
				t.Fatalf("work outcome invented readiness or released teardown: %+v", snapshot)
			}
			if err := owner.requestCleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-joined:
				// This fixture's actual helper stack has returned. It does not
				// represent completion of a real NVKE deployment stack.
				owner.recordTeardownReturn(err)
				if err != nil || !cleaned.Load() || work.Err() == nil {
					t.Fatalf("explicit cleanup did not release/join: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("explicit cleanup did not join held work")
			}
		})
	}
}

func TestRetainedRuntimeRejectsViewerOwnedLifetimeBeforeWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	deadline, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code nvkrerr.Code
	}{
		{"nil", nil, nvkrerr.CodeUsage},
		{"canceled", canceled, nvkrerr.CodeTimeout},
		{"viewer", t.Context(), nvkrerr.CodeUsage},
		{"deadline", deadline, nvkrerr.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, err := newRetainedRuntime(test.ctx, func() { t.Error("unadmitted owner canceled work") })
			var coded *nvkrerr.Error
			if owner != nil || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("viewer lifetime became a retained service owner: %v", err)
			}
		})
	}
}

func TestRetainedControlObservationCannotChangeState(t *testing.T) {
	owner, _, _ := retainedTestOwner(t)
	short, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	control := &retainedControl{owner: owner}
	if got, err := control.snapshot(short); got.State != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unready observer invented state", got, err)
	}
	owner.mu.Lock()
	state := owner.snapshot.state
	owner.mu.Unlock()
	if state != retainedStarting {
		t.Fatal("viewer timeout changed runtime state")
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := owner.requestCleanup(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("expired cleanup request accepted", err)
	}
	select {
	case <-owner.cleanup:
		t.Fatal("expired request released cleanup")
	default:
	}
}

func TestRetainedRuntimeUnissuedDescriptionsAndReadinessTransitionsFailClosed(t *testing.T) {
	for _, owner := range []*retainedRuntime{nil, {}} {
		if err := owner.hold(); err == nil {
			t.Fatal("unissued description permitted a hold")
		}
		if err := owner.requestCleanup(t.Context()); err == nil {
			t.Fatal("unissued description permitted cleanup")
		}
	}
	for _, state := range []retainedRuntimeState{retainedPartial, retainedReady, retainedCleanupRequested, "unknown"} {
		t.Run(string(state), func(t *testing.T) {
			owner, work, _ := retainedTestOwner(t)
			owner.mu.Lock()
			owner.snapshot.state = state
			owner.mu.Unlock()
			err := owner.markReady(work, OperatorAccess{kubectl: "/tool", site: "/site", tenant: "/tenant"})
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
				t.Fatalf("readiness replaced a non-starting state: %v", err)
			}
			if state == "unknown" {
				observation, stop := context.WithTimeout(t.Context(), time.Second)
				defer stop()
				control := &retainedControl{owner: owner}
				if got, err := control.snapshot(observation); got.State != "" || !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
					t.Fatal("unknown snapshot returned as an admitted observation", got, err)
				}
				if err := owner.requestCleanup(t.Context()); !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
					t.Fatal("unknown owner state allowed cleanup", err)
				}
				if err := owner.hold(); !errors.Is(err, ErrRetainedControls) {
					t.Fatal("unknown owner state released teardown", err)
				}
				owner.mu.Lock()
				owner.snapshot.state = retainedPartial
				owner.mu.Unlock()
			}
		})
	}
}

func TestRetainedRuntimeRealServiceOutlivesCanceledWorkUntilJoinedCleanup(t *testing.T) {
	owner, work, cancelWork := retainedTestOwner(t)
	runner := Runner{Work: privateTestDir(t), Env: []string{"PATH=/usr/bin:/bin"}}
	process, err := runner.Start(work, []string{"/bin/sleep", "60"}, "retained-fixture")
	if err != nil {
		t.Fatal(err)
	}
	// Fixture failure still has exactly one actual process owner; never release
	// an os/exec handle or leave a Wait goroutine after this test returns.
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer stop()
		if err := process.Stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	cancelWork()
	if exited, err := process.Exited(); exited || err != nil {
		t.Fatalf("operation cancellation destroyed retained service: %v", err)
	}
	if err := owner.requestCleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	cleanup, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if err := process.Stop(cleanup); err != nil {
		t.Fatal(err)
	}
	if exited, err := process.Exited(); !exited || errors.Is(err, ErrUnjoined) {
		t.Fatal("explicit stop did not join actual service", err)
	}
}

func TestRetainedCleanupFailureCannotBeReplacedWithPrimarySuccess(t *testing.T) {
	owner, work, _ := retainedTestOwner(t)
	if err := owner.joinedCleanup(); !errors.Is(err, ErrRetainedControls) {
		t.Fatal("starting owner manufactured joined cleanup", err)
	}
	if err := owner.requestCleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := owner.joinedCleanup(); !errors.Is(err, ErrRetainedControls) {
		t.Fatal("request alone supplied a joined teardown", err)
	}
	root := privateTestDir(t)
	writeTenantFixture(t, filepath.Join(root, "kind"), []byte("#!/bin/sh\nexit 7\n"), 0o700)
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}}
	attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root}, bootstrapRunner: &runner, bootstrapName: "owned"}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(work), 5*time.Second)
	defer stop()
	failure := attempt.close(cleanup)
	if CommandExitCode(failure) != 7 {
		t.Fatal("native cleanup fixture did not return its original failure", failure)
	}
	recordRetainedCleanup(work, failure)
	owner.recordTeardownReturn(nil)
	if !errors.Is(owner.joinedCleanup(), failure) {
		t.Fatal("successful operation hid actual native cleanup failure", owner.joinedCleanup())
	}
	recordRetainedCleanup(work, nil)
	if !errors.Is(owner.joinedCleanup(), failure) {
		t.Fatal("later successful observation erased failed cleanup")
	}
}
