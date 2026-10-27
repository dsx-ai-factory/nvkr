// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// These states describe a live downstream owner, not persisted authority,
// current Kubernetes health, or full E2E acceptance. No serialized snapshot can
// construct this owner or request cleanup. The future control server must bind
// requests to its own exact live session before calling requestCleanup.
type retainedRuntimeState string

const (
	retainedStarting         retainedRuntimeState = "starting"
	retainedPartial          retainedRuntimeState = "held-partial"
	retainedReady            retainedRuntimeState = "retained-ready"
	retainedCleanupRequested retainedRuntimeState = "cleanup-requested"
)

type retainedRuntimeSnapshot struct {
	state     retainedRuntimeState
	checkedAt time.Time
	access    OperatorAccess
}

// A retainedRuntime must not be value-copied. The service owner holds it until
// explicit cleanup, independently of the foreground presenter/work context.
// It does not detach processes or transfer sudo authorization by itself.
type retainedRuntime struct {
	mu               sync.Mutex
	lifetime         context.Context
	cancelOperations context.CancelFunc
	cleanup          chan struct{}
	snapshot         retainedRuntimeSnapshot
	cleanupErr       error
	teardownReturned bool
	networkStarted   bool
	// Installed by the actual service owner before launching its work. The
	// first private failure must be inspectable while teardown is held.
	failureRecorder func(error) error
	failureOnce     sync.Once
	failureErr      error
	failureCode     nvkrerr.Code
	panicRecorder   func([]byte) error
}

func noteRetainedNetworkStart(ctx context.Context) {
	if owner := runtimeRetention(ctx); owner != nil {
		owner.mu.Lock()
		owner.networkStarted = true
		owner.mu.Unlock()
	}
}

type retainedRuntimeKey struct{}

func newRetainedRuntime(lifetime context.Context, cancelOperations context.CancelFunc) (*retainedRuntime, error) {
	if lifetime == nil || cancelOperations == nil {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retention requires an independent owner context and owned operation cancellation")
	}
	if err := lifetime.Err(); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained owner unavailable before admission")
	}
	if _, deadline := lifetime.Deadline(); deadline || lifetime.Done() != nil {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained owner lifetime must be independent of viewer cancellation and deadlines")
	}
	return &retainedRuntime{lifetime: lifetime, cancelOperations: cancelOperations, cleanup: make(chan struct{}), snapshot: retainedRuntimeSnapshot{state: retainedStarting}}, nil
}

func (owner *retainedRuntime) issued() bool {
	// Only immutable constructor fields may be inspected outside the lock.
	return owner != nil && owner.lifetime != nil && owner.cancelOperations != nil && owner.cleanup != nil
}

func runtimeRetention(ctx context.Context) *retainedRuntime {
	owner, _ := ctx.Value(retainedRuntimeKey{}).(*retainedRuntime)
	return owner
}

// markReady is called only after the tenant owner has completed its exact real
// node, disk, workload and DNS checks, then inspected this session's access.
// Access alone is not sufficient. This private method is not a receipt factory
// for callers and does not enable daemon or CLI retention on its own.
func (owner *retainedRuntime) markReady(ctx context.Context, access OperatorAccess) error {
	if !owner.issued() || ctx == nil || access.kubectl == "" || access.site == "" || access.tenant == "" {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained readiness requires the live owner and inspected session access")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := errors.Join(ctx.Err(), owner.lifetime.Err()); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained readiness canceled before observation")
	}
	if owner.snapshot.state != retainedStarting {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained readiness cannot replace a held or cleanup state")
	}
	owner.snapshot = retainedRuntimeSnapshot{state: retainedReady, checkedAt: time.Now(), access: access}
	return nil
}

// requestCleanup is the only release of the hold. It is idempotent after the
// first request, but refuses an unissued owner or an expired request. Losing a
// viewer, a canceled operation or a timeout never calls this method implicitly.
func (owner *retainedRuntime) requestCleanup(ctx context.Context) error {
	if !owner.issued() || ctx == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "cleanup requires an issued live retained owner and request context")
	}
	owner.mu.Lock()
	if err := errors.Join(ctx.Err(), owner.lifetime.Err()); err != nil {
		owner.mu.Unlock()
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "cleanup request expired before owner admission")
	}
	first := owner.snapshot.state != retainedCleanupRequested
	if first {
		switch owner.snapshot.state {
		case retainedStarting, retainedPartial, retainedReady:
		default:
			owner.mu.Unlock()
			return nvkrerr.New(nvkrerr.CodePreflightFailed, "cleanup refused an unknown retained owner state")
		}
		owner.snapshot.state = retainedCleanupRequested
		close(owner.cleanup)
	}
	owner.mu.Unlock()
	// Release the state lock before cancellation reaches a callback. No
	// external effect, root command or resource deletion runs under this lock.
	if first {
		owner.cancelOperations()
	}
	return nil
}

// hold runs before every nested dependent teardown. A nil primary result here
// cannot invent readiness: only markReady above records completed observations.
// An owner failure returns retained-controls, causing teardown to fail closed.
func (owner *retainedRuntime) hold() error {
	if !owner.issued() {
		return nvkrerr.New(nvkrerr.CodeUsage, "cannot hold an unissued retained owner")
	}
	owner.mu.Lock()
	switch owner.snapshot.state {
	case retainedStarting, retainedPartial, retainedReady, retainedCleanupRequested:
	default:
		owner.mu.Unlock()
		return errors.Join(ErrRetainedControls, nvkrerr.New(nvkrerr.CodePreflightFailed, "teardown refused an unknown retained owner state"))
	}
	if owner.snapshot.state == retainedStarting {
		owner.snapshot.state = retainedPartial
	}
	owner.mu.Unlock()
	select {
	case <-owner.cleanup:
		return nil
	case <-owner.lifetime.Done():
		// Even when both channels are ready, only an actual issued request can
		// permit cleanup. Owner cancellation alone has no teardown authority.
		owner.mu.Lock()
		requested := owner.snapshot.state == retainedCleanupRequested
		owner.mu.Unlock()
		if requested {
			return nil
		}
		return errors.Join(ErrRetainedControls, nvkrerr.Wrap(nvkrerr.CodeTimeout, owner.lifetime.Err(), "retained owner ended without explicit cleanup"))
	}
}

func (owner *retainedRuntime) retainFailure(primary error) error {
	if primary == nil {
		return nil
	}
	owner.failureOnce.Do(func() {
		code := nvkrerr.CodePreflightFailed
		var coded *nvkrerr.Error
		if errors.As(primary, &coded) {
			code = coded.Code
		}
		if errors.Is(primary, context.Canceled) || errors.Is(primary, context.DeadlineExceeded) {
			code = nvkrerr.CodeTimeout
		}
		owner.mu.Lock()
		owner.failureCode = code
		owner.mu.Unlock()
		if owner.failureRecorder != nil {
			owner.failureErr = owner.failureRecorder(primary)
		}
	})
	return owner.failureErr
}

func waitRetainedCleanup(ctx context.Context, primary ...error) error {
	if owner := runtimeRetention(ctx); owner != nil {
		// Diagnostic I/O failure is not a refused ownership hold. In
		// particular ENOSPC must not skip explicit natural teardown. The
		// entry owner preserves failureErr separately after the stack returns.
		_ = owner.retainFailure(errors.Join(primary...))
		return owner.hold()
	}
	return nil
}

// Register directly after a dependent hold's defer. Retained panic diagnostics
// must exist before that hold blocks unwinding. Foreground recovery is unchanged;
// Custom payload methods are never invoked; no value enters the public channel.
// This scope still owns its registered cleanup handles: a recovered operation
// panic must not invent unknown ownership and skip their explicit teardown.
// Existing unjoined/retained-control errors and the outer entry recovery remain
// fail-closed; this helper grants neither joined cleanup nor host restoration.
func recoverRetainedPanic(ctx context.Context, result *error) {
	owner := runtimeRetention(ctx)
	if owner == nil {
		return
	}
	if value := recover(); value != nil {
		stack := debug.Stack()
		stack = stack[:min(len(stack), 1<<20)]
		var storeErr error
		if owner.panicRecorder != nil {
			storeErr = owner.panicRecorder(fmt.Appendf(nil, "Panic: %s\n\n%s", PrivatePanicValueDescription(value), stack))
		}
		*result = errors.Join(*result, nvkrerr.Wrap(nvkrerr.CodeInternal, storeErr, "retained operation panic; inspect private diagnostics"))
		_ = owner.retainFailure(*result)
	}
}

// Only the actual teardown stack records these errors. An operation failure is
// separate from cleanup failure: a canceled build does not assert that joined
// services failed to stop, and a JSON response cannot manufacture this result.
func recordRetainedCleanup(ctx context.Context, err error) {
	if owner := runtimeRetention(ctx); owner != nil && err != nil {
		owner.mu.Lock()
		owner.cleanupErr = errors.Join(owner.cleanupErr, err)
		owner.mu.Unlock()
	}
}

func (owner *retainedRuntime) joinedCleanup() error {
	if !owner.issued() {
		return nvkrerr.New(nvkrerr.CodeUsage, "cleanup result requires its live retained owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.snapshot.state != retainedCleanupRequested || !owner.teardownReturned {
		return errors.Join(ErrRetainedControls, nvkrerr.New(nvkrerr.CodePreflightFailed, "explicit cleanup and actual teardown return have not both been observed"))
	}
	return owner.cleanupErr
}

// The service owner calls this only after the entire actual Run stack returns,
// including nested finalizers. A request, timer or live process cannot supply
// this completion boundary. Original operation errors stay separately private.
func (owner *retainedRuntime) recordTeardownReturn(primary error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if errors.Is(primary, ErrUnjoined) || errors.Is(primary, ErrRetainedControls) {
		owner.cleanupErr = errors.Join(owner.cleanupErr, primary)
	}
	owner.teardownReturned = true
}
