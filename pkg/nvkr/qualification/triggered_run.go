// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// TriggeredRun owns the authenticated source, invocation, artifact checks and
// smoke for one publication. Copies share cancellation and closure. It cannot
// be reconstructed from a Publication or a serialized evidence document.
type TriggeredRun struct{ state *triggeredRunState }

type triggeredRunState struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	attempted bool
	closeErr  error
	closers   []func() error
	publish   func(context.Context) (*Publication, error)
	checks    *lifecycle.ArtifactCheckReceipt
	evidence  string
}

func newTriggeredRun(ctx context.Context) *TriggeredRun {
	owned, cancel := WithHostedRuntimeBudget(ctx)
	return &TriggeredRun{state: &triggeredRunState{ctx: owned, cancel: cancel}}
}

// Publish performs at most one qualification publication. Failed invocations
// retain their diagnostic result but cannot expose publication inputs for M4.
func (run *TriggeredRun) Publish(ctx context.Context) (*Publication, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification publication requires context")
	}
	if run == nil || run.state == nil {
		return nil, e.New(e.CodeGateUnmet, "qualification operation is not open")
	}
	state := run.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.publish == nil || state.attempted {
		return nil, e.New(e.CodeGateUnmet, "qualification operation is closed, unavailable or already attempted")
	}
	if err := state.ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification operation canceled")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification publication canceled")
	}
	state.attempted = true
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.ctx, cancel)
	defer func() { stop(); cancel() }()
	result, err := state.publish(operation)
	if err != nil {
		return result, err
	}
	if err := operation.Err(); err != nil {
		if result != nil {
			result.Successful = false
		}
		return result, e.Wrap(e.CodeTimeout, err, "qualification publication interrupted; attachments may remain")
	}
	if result == nil || result.Evidence == nil {
		return result, e.New(e.CodeGateUnmet, "qualification completed without exact evidence")
	}
	raw, err := result.Evidence.Canonical()
	if err != nil {
		return result, err
	}
	// Freeze before returning the mutable diagnostic projection. The live
	// artifact receipt and later independent verification remain authoritative.
	state.evidence = string(raw)
	return result, nil
}

// PublishedEvidence borrows this operation's live artifact receipt and returns
// a copy of its exact completed predicate bytes. Neither output survives Close
// as an authority: the issuer must still authenticate every registry attachment.
func (run *TriggeredRun) PublishedEvidence() (*lifecycle.ArtifactCheckReceipt, []byte, error) {
	if run == nil || run.state == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "qualification operation is not open")
	}
	state := run.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.evidence == "" || state.checks == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "qualification publication has not completed with live artifact checks")
	}
	if err := state.ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "qualification operation canceled")
	}
	if _, err := state.checks.Description(); err != nil {
		return nil, nil, err
	}
	return state.checks, []byte(state.evidence), nil
}

// Close cancels and joins an active publication, then releases borrowed
// capabilities in reverse dependency order. Repeated calls share its outcome.
func (run *TriggeredRun) Close() error {
	if run == nil || run.state == nil {
		return nil
	}
	state := run.state
	state.cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeErr
	}
	state.closed = true
	for i := len(state.closers) - 1; i >= 0; i-- {
		state.closeErr = errors.Join(state.closeErr, state.closers[i]())
	}
	state.closers = nil
	state.checks = nil
	state.evidence = ""
	return state.closeErr
}
