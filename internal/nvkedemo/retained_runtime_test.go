// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
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
	snapshot, err := owner.observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
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
				result := func() (result error) {
					defer func() { result = errors.Join(result, waitRetainedCleanup(work, result)) }()
					defer recoverRetainedPanic(work, &result)
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
				}()
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

func TestRetainedNestedPanicCleansKnownHandlesAfterExplicitRequest(t *testing.T) {
	owner, work, _ := retainedTestOwner(t)
	privatePath := filepath.Join(privateTestDir(t), "owner-panic.log")
	owner.panicRecorder = func(raw []byte) error { return privateWrite(privatePath, raw) }
	control := &retainedControl{owner: owner, finish: make(chan struct{}),
		description: retainedControlDescription{Work: "/owned-work", Selector: "/owned-session", Token: "fixture-token"}}
	stages := []string{"runtime", "management", "registry", "tenant"}
	var order []string
	var consume func(int) error
	consume = func(level int) (result error) {
		// Match the owning scopes' defer order. Registry and management use
		// the actual shared cleanup helper; tenant/runtime retain their guards.
		defer func() {
			cleanup := func(context.Context) error {
				order = append(order, stages[level])
				return nil
			}
			if stages[level] == "registry" || stages[level] == "management" {
				result = cleanupAfterJoinedWork(work, result, time.Second, cleanup)
				return
			}
			if holdErr := waitRetainedCleanup(work, result); holdErr != nil {
				result = errors.Join(result, holdErr)
				return
			}
			if errors.Is(result, ErrUnjoined) || stages[level] == "runtime" && errors.Is(result, ErrRetainedControls) {
				recordRetainedCleanup(work, result)
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(work), time.Second)
			defer cancel()
			cleanupErr := cleanup(cleanupCtx)
			recordRetainedCleanup(work, cleanupErr)
			result = errors.Join(result, cleanupErr)
		}()
		defer recoverRetainedPanic(work, &result)
		if level == len(stages)-1 {
			panic("PRIVATE-NESTED-PANIC")
		}
		return consume(level + 1)
	}
	joined := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		joined <- consume(0)
	}()
	defer func() {
		_ = owner.requestCleanup(context.WithoutCancel(t.Context()))
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("nested panic fixture did not join")
		}
	}()
	if snapshot := retainedObserve(t, owner); snapshot.state != retainedPartial {
		t.Fatal("nested panic did not hold before teardown", snapshot)
	}
	private, err := os.ReadFile(privatePath)
	state, stateErr := control.snapshot(t.Context())
	if err != nil || !bytes.Contains(private, []byte("PRIVATE-NESTED-PANIC")) || stateErr != nil || state.FailureCode != nvkrerr.CodeInternal || state.TeardownReturned {
		t.Fatal("panic diagnostics unavailable before explicit cleanup", err, state, stateErr)
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
	if status := request("/cleanup"); status != http.StatusOK {
		t.Fatal("issued cleanup request was refused", status)
	}
	select {
	case result := <-joined:
		var coded *nvkrerr.Error
		if !errors.As(result, &coded) || coded.Code != nvkrerr.CodeInternal || errors.Is(result, ErrRetainedControls) || !slices.Equal(order, []string{"tenant", "registry", "management", "runtime"}) {
			t.Fatal("nested panic skipped known cleanup or lost its operation failure", order, result)
		}
		owner.recordTeardownReturn(result)
		if err := control.recordJoined(t.Context(), nil); err != nil || request("/finish") != http.StatusOK {
			t.Fatal("known joined cleanup did not permit owner finish", err)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit cleanup did not join the nested panic chain")
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

func TestRetainedRuntimeNestedTeardownIsReleasedOnlyOnce(t *testing.T) {
	owner, work, _ := retainedTestOwner(t)
	var order []string
	joined := make(chan error, 1)
	go func() {
		var result error
		// This fixture mirrors the four owning defers, without giving a fake
		// process or serialized state permission over production resources.
		defer func() { joined <- result }()
		for _, stage := range []string{"runtime", "management", "registry", "tenant"} {
			defer func() {
				if err := waitRetainedCleanup(work); err != nil {
					result = errors.Join(result, err)
					return
				}
				order = append(order, stage)
			}()
		}
	}()
	if got := retainedObserve(t, owner); got.state != retainedPartial {
		t.Fatal("missing held partial state")
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
		if err != nil || !slices.Equal(order, []string{"tenant", "registry", "management", "runtime"}) {
			t.Fatalf("dependent teardown did not join exactly once: %v %v", order, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nested owner did not join")
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

func TestRetainedRuntimeObservationCannotChangeState(t *testing.T) {
	owner, _, _ := retainedTestOwner(t)
	short, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if got, err := owner.observe(short); got != (retainedRuntimeSnapshot{}) || !errors.Is(err, context.DeadlineExceeded) {
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
				if got, err := owner.observe(observation); got != (retainedRuntimeSnapshot{}) || !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
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

func TestRetainedProductionCleanupHelperPreservesPrimaryAndUnjoined(t *testing.T) {
	for _, mode := range []string{"success", "coded failure", "unjoined", "retained controls"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, cancel := retainedTestOwner(t)
			var primary error
			switch mode {
			case "coded failure":
				primary = nvkrerr.New(nvkrerr.CodeFetchFailed, "original fixture failure")
			case "unjoined":
				primary = errors.Join(ErrUnjoined, nvkrerr.New(nvkrerr.CodeBuildFailed, "original fixture failure"))
			case "retained controls":
				primary = ErrRetainedControls
			}
			cancel()
			var cleanupCalls atomic.Int32
			joined := make(chan error, 1)
			go func() {
				joined <- cleanupAfterJoinedWork(work, primary, time.Second, func(cleanup context.Context) error {
					cleanupCalls.Add(1)
					if cleanup.Err() != nil {
						return cleanup.Err()
					}
					if _, bounded := cleanup.Deadline(); !bounded {
						return errors.New("fixture cleanup was unbounded")
					}
					return nil
				})
			}()
			if got := retainedObserve(t, owner); got.state != retainedPartial || cleanupCalls.Load() != 0 {
				t.Fatal("actual production helper cleaned before explicit request", got)
			}
			if err := owner.requestCleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-joined:
				if primary != nil && !errors.Is(err, primary) || primary == nil && err != nil {
					t.Fatal("actual production helper discarded original failure", err)
				}
				owner.recordTeardownReturn(err)
				want := int32(1)
				if mode == "unjoined" || mode == "retained controls" {
					want = 0
				}
				if cleanupCalls.Load() != want {
					t.Fatalf("dependent controls removed around unjoined work: %d", cleanupCalls.Load())
				}
				if cleanupErr := owner.joinedCleanup(); (cleanupErr == nil) != (want == 1) {
					t.Fatal("operation error was confused with teardown result", cleanupErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("actual production helper did not return")
			}
		})
	}
}

func TestRetainedTeardownLeavesPrivilegedControlsForForegroundRestore(t *testing.T) {
	for _, mode := range []string{"idle", "foreign domain", "inventory failed", "DNS unjoined", "not requested"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, _ := retainedTestOwner(t)
			guard := networkFixture(t)
			guard.BGPIntent, guard.BridgeIntent = true, true
			rootCalls, stoppedDNS := 0, 0
			guard.capture = func(context.Context, []string, bool) ([]byte, error) {
				rootCalls++
				return nil, errors.New("retained process must not invoke host restoration")
			}
			guard.stopDNSHook = func(context.Context) error {
				stoppedDNS++
				if mode == "DNS unjoined" {
					return ErrUnjoined
				}
				return nil
			}
			tools := privateTestDir(t)
			body := "#!/bin/sh\nexit 0\n"
			switch mode {
			case "foreign domain":
				body = "#!/bin/sh\nprintf 'foreign-domain\\n'\n"
			case "inventory failed":
				body = "#!/bin/sh\nexit 2\n"
			}
			if err := os.WriteFile(filepath.Join(tools, "virsh"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if mode != "not requested" {
				if err := owner.requestCleanup(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			ctx, stop := context.WithTimeout(context.WithoutCancel(work), 3*time.Second)
			defer stop()
			err := cleanupRetainedRuntime(ctx, nil, nil, guard, guard.Work, Runner{Env: []string{"PATH=" + tools + ":/usr/bin:/bin"}})
			if (err == nil) != (mode == "idle") || rootCalls != 0 || !guard.BGPIntent || !guard.BridgeIntent || guard.finished {
				t.Fatal("retained teardown changed privileged controls or overclaimed completion", err, rootCalls)
			}
			wantDNS := 1
			if mode == "not requested" {
				wantDNS = 0
			}
			if stoppedDNS != wantDNS || mode == "DNS unjoined" && !errors.Is(err, ErrUnjoined) {
				t.Fatal("unjoined producer was discarded or unrequested cleanup ran", err, stoppedDNS)
			}
		})
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
	failure := errors.New("fixture teardown failed")
	if err := owner.joinedCleanup(); !errors.Is(err, ErrRetainedControls) {
		t.Fatal("request alone supplied a joined teardown", err)
	}
	result := cleanupAfterJoinedWork(work, nil, time.Second, func(context.Context) error { return failure })
	owner.recordTeardownReturn(result)
	if !errors.Is(result, failure) || !errors.Is(owner.joinedCleanup(), failure) {
		t.Fatal("successful operation hid failed cleanup", result, owner.joinedCleanup())
	}
	recordRetainedCleanup(work, nil)
	if !errors.Is(owner.joinedCleanup(), failure) {
		t.Fatal("later successful observation erased failed cleanup")
	}
}
