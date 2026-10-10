// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SourceWorkspace owns filesystem access for a hosted source fetch, not build,
// evidence or release authority. Only HoldSourceWorkspace issues an open handle.
type SourceWorkspace struct {
	state *sourceWorkspaceState
}

type sourceWorkspaceState struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	root     *os.File
	closed   bool
	done     chan struct{}
	active   sync.WaitGroup
	calls    map[context.Context]context.CancelFunc
	closeErr error
}

// HoldSourceWorkspace admits inherited-directory execution before opening the
// current workspace. It never creates a directory or authenticates source bytes.
func HoldSourceWorkspace(ctx context.Context) (*SourceWorkspace, error) {
	if err := requireRunContext(ctx, "source workspace"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "source workspace admission canceled")
	}
	if _, err := sourceFetchDescriptorPath(3); err != nil {
		return nil, err
	}
	root, _, err := securepath.OpenDirectory(".")
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "holding source fetch workspace")
	}
	owned, cancel := context.WithCancel(ctx)
	return &SourceWorkspace{state: &sourceWorkspaceState{ctx: owned, cancel: cancel, root: root,
		done: make(chan struct{}), calls: make(map[context.Context]context.CancelFunc)}}, nil
}

// borrow registers a complete fetch before Close can cancel its children. The
// caller releases only after every Git/runtime command and cleanup has joined.
func (workspace *SourceWorkspace) borrow(ctx context.Context) (context.Context, *os.File, func(), error) {
	if err := requireRunContext(ctx, "source workspace fetch"); err != nil {
		return nil, nil, nil, err
	}
	if workspace == nil || workspace.state == nil {
		return nil, nil, nil, e.New(e.CodeEngineCapability, "source workspace was not issued by the pipeline")
	}
	state := workspace.state
	state.mu.Lock()
	if state.closed || state.root == nil {
		state.mu.Unlock()
		return nil, nil, nil, e.New(e.CodeEngineCapability, "source workspace is closed")
	}
	if err := state.ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, nil, nil, e.Wrap(e.CodeTimeout, err, "source workspace owner canceled")
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, nil, nil, e.Wrap(e.CodeTimeout, err, "source workspace fetch canceled")
	}
	call, cancel := context.WithCancel(ctx)
	state.calls[call] = cancel
	state.active.Add(1)
	root := state.root
	state.mu.Unlock()
	stop := context.AfterFunc(state.ctx, cancel)
	release := func() {
		stop()
		cancel()
		state.mu.Lock()
		delete(state.calls, call)
		state.mu.Unlock()
		state.active.Done()
	}
	return call, root, release, nil
}

// Close revokes new calls, synchronously cancels admitted calls, and waits for
// their stop/join before releasing the directory. Repeated closes share a result.
func (workspace *SourceWorkspace) Close() error {
	if workspace == nil || workspace.state == nil {
		return nil
	}
	state := workspace.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		<-state.done
		return state.closeErr
	}
	state.closed = true
	for _, cancel := range state.calls {
		cancel()
	}
	state.cancel()
	state.mu.Unlock()
	state.active.Wait()
	if err := state.root.Close(); err != nil {
		state.closeErr = e.Wrap(e.CodeFetchFailed, err, "closing source workspace")
	}
	close(state.done)
	return state.closeErr
}
