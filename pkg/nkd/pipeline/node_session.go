// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"path/filepath"
	"sync"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// NodeSession retains prepared source snapshots while an owning scheduler runs
// individual producers. It never schedules work or reuses producer results.
// Producer contexts inherit the caller's values and deadline; Close cancels
// admitted calls synchronously before the session context, so a resumed
// producer cannot begin new runtime work. Close then joins active producers
// before releasing their shared snapshots.
type NodeSession struct {
	state *nodeSessionState
}

type nodeSessionState struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	done          chan struct{}
	active        sync.WaitGroup
	calls         map[context.Context]context.CancelFunc
	sources       map[string]*preparedSourceCache
	packerRuntime *nodeSessionPackerRuntime
}

func NewNodeSession(ctx context.Context) (*NodeSession, error) {
	if err := requireRunContext(ctx, "node execution session"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	owned, cancel := context.WithCancel(ctx)
	return &NodeSession{state: &nodeSessionState{ctx: owned, cancel: cancel, done: make(chan struct{}),
		calls: make(map[context.Context]context.CancelFunc), sources: make(map[string]*preparedSourceCache)}}, nil
}

func (handle *NodeSession) ExecNode(node ir.Node, options RunOptions) (*ArtifactResult, error) {
	if err := requireRunContext(options.Ctx, "node execution session"); err != nil {
		return nil, err
	}
	if handle == nil || handle.state == nil {
		return nil, e.New(e.CodeEngineCapability, "node execution session is not open")
	}
	if !options.Launch {
		return nil, e.New(e.CodeEngineCapability, "node execution session requires launched producers")
	}
	if options.Qualification != nil {
		return nil, e.New(e.CodeEngineCapability, "qualified execution requires the complete Run admission protocol")
	}
	options = withDefaults(options)
	session := handle.state
	root, err := filepath.Abs(options.SourceRoot)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving prepared source owner")
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil, e.New(e.CodeEngineCapability, "node execution session is closed")
	}
	if err := session.ctx.Err(); err != nil {
		session.mu.Unlock()
		return nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	if node.Kind == ir.KindBuild {
		cache := session.sources[root]
		if cache == nil {
			cache = newPreparedSourceCache()
			session.sources[root] = cache
		}
		options.sources = cache
	}
	// Retain the caller's values, deadline, and synchronous cancellation while
	// registering this owned child before Close can begin canceling active work.
	ctx, cancel := context.WithCancel(options.Ctx)
	session.calls[ctx] = cancel
	session.active.Add(1)
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		delete(session.calls, ctx)
		session.mu.Unlock()
		session.active.Done()
	}()
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()
	options.Ctx = ctx
	return (Local{}).ExecNode(node, options)
}

func (handle *NodeSession) Close() {
	if handle == nil || handle.state == nil {
		return
	}
	session := handle.state
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		<-session.done
		return
	}
	session.closed = true
	// AfterFunc propagates owner-parent cancellation, but its callback is
	// asynchronous. Close must cancel admitted calls before exposing session
	// cancellation, so a resumed producer cannot start new runtime work.
	for _, cancel := range session.calls {
		cancel()
	}
	session.cancel()
	session.mu.Unlock()
	session.active.Wait()
	for _, cache := range session.sources {
		cache.close()
	}
	if session.packerRuntime != nil {
		<-session.packerRuntime.ready
		if session.packerRuntime.runtime != nil && session.packerRuntime.runtime.capability != nil {
			_ = session.packerRuntime.runtime.close()
		}
	}
	close(session.done)
}
