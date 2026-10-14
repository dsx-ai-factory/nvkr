// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// runContainerRuntime is the only subprocess boundary for runtime-backed build
// operations. Qualified execution dispatches through the held executable
// capability; development and prequalification acquisition retain the configured
// runtime pathname behavior.
func runContainerRuntime(opts RunOptions, args ...string) error {
	if opts.execution != nil && opts.execution.runtime != nil {
		return opts.execution.runtime.run(opts.ctx(), opts.Stdout, opts.Stderr, args...)
	}
	if opts.debianRuntime != nil {
		return opts.debianRuntime.run(opts.ctx(), opts.Stdout, opts.Stderr, args...)
	}
	cmd := execCmd(opts, opts.Runtime, args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return runOwnedCommand(opts.ctx(), cmd)
}

// reaper is the container cleanup registry (ADR-17 §2): every started container is
// registered by name and receives a bounded force-removal attempt on context cancel
// or Run exit. Cleanup never waits indefinitely for an unresponsive daemon.
type reaper struct {
	mu             sync.Mutex
	names          map[string]bool
	docker         string
	runtime        *heldRuntimeExecutable
	cleanupCommand reaperCleanupCommand
	closing        bool
	cleanupTimeout time.Duration
	stop           chan struct{}
	done           chan struct{}
	stopOnce       sync.Once
	cleanupErr     error
}

const (
	defaultReaperCleanupTimeout = 2 * time.Second
	reaperTerminationGrace      = 250 * time.Millisecond
)

func newReaper(ctx context.Context, docker string) *reaper {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &reaper{
		names: map[string]bool{}, docker: docker,
		cleanupTimeout: defaultReaperCleanupTimeout,
		cleanupCommand: runReaperCleanupCommand,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	go r.watch(ctx)
	return r
}

func (r *reaper) watch(ctx context.Context) {
	defer close(r.done)
	select {
	case <-ctx.Done():
	case <-r.stop:
	}
	r.cleanup()
}

// add registers a container before its runtime subprocess starts. Once cleanup
// begins no new work may enter the registry: the caller must fail without starting
// its subprocess so the cleanup snapshot remains exhaustive.
func (r *reaper) add(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.names[name] = true
	return true
}

// forget removes a container only after its runtime command succeeded. Failed or
// canceled commands stay registered because the client may have exited while the
// daemon-side container continued running.
func (r *reaper) forget(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.names, name)
	r.mu.Unlock()
}

func (r *reaper) setRuntime(runtimePath string, runtime *heldRuntimeExecutable) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.docker = runtimePath
	r.runtime = runtime
	r.mu.Unlock()
}

func (r *reaper) close() {
	_ = r.closeChecked()
}

// The retained-producer boundary must observe cleanup failures before
// transferring bytes. Legacy callers retain their best-effort close behavior.
func (r *reaper) closeChecked() error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
	return r.cleanupErr
}

// cleanup takes one closed-world snapshot under the registry mutex, then performs
// all subprocess I/O without the mutex. add rejects later registrations, so the
// snapshot cannot miss a container that is allowed to start. One context bounds the
// entire best-effort cleanup rather than multiplying the timeout by container count.
func (r *reaper) cleanup() {
	r.mu.Lock()
	r.closing = true
	names := make([]string, 0, len(r.names))
	for name := range r.names {
		names = append(names, name)
	}
	sort.Strings(names)
	docker, runtimeExecutable := r.docker, r.runtime
	cleanupCommand := r.cleanupCommand
	timeout := r.cleanupTimeout
	r.mu.Unlock()

	if len(names) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = defaultReaperCleanupTimeout
	}
	if cleanupCommand == nil {
		cleanupCommand = runReaperCleanupCommand
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	grace := reaperTerminationGrace
	if grace >= remaining {
		grace = remaining / 2
	}
	if grace <= 0 {
		r.cleanupErr = e.New(e.CodeTimeout, "container cleanup has no remaining termination budget")
		return
	}
	// Every registered container receives an independent removal attempt under the
	// same overall deadline. One unresponsive client cannot prevent the other names
	// from reaching the daemon.
	commandCtx, commandCancel := context.WithDeadline(ctx, deadline.Add(-grace))
	defer commandCancel()
	var cleanup sync.WaitGroup
	failures := make([]error, len(names))
	cleanup.Add(len(names))
	for index, name := range names {
		go func() {
			defer cleanup.Done()
			if err := cleanupCommand(commandCtx, grace, docker, runtimeExecutable, []string{name}); err != nil {
				failures[index] = e.Wrap(e.CodeBuildFailed, err, "removing owned container %q", name)
			}
		}()
	}
	cleanup.Wait()
	r.cleanupErr = errors.Join(failures...)
}

type reaperCleanupCommand func(context.Context, time.Duration, string, *heldRuntimeExecutable, []string) error

func runReaperCleanupCommand(ctx context.Context, grace time.Duration, docker string,
	runtimeExecutable *heldRuntimeExecutable, names []string,
) error {
	args := append([]string{"rm", "-f"}, names...)
	if runtimeExecutable != nil {
		return runtimeExecutable.runCleanup(ctx, grace, nil, nil, args...)
	}
	cmd := execCmd(RunOptions{Ctx: ctx}, docker, args...) //nvkr:allow-nondeterminism best-effort cleanup, not identity
	cmd.WaitDelay = grace
	return runOwnedCommand(ctx, cmd)
}

func registerContainer(opts RunOptions, name string) error {
	if opts.reap != nil && opts.reap.add(name) {
		return nil
	}
	if err := opts.ctx().Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "container cleanup began before %q could start", name)
	}
	return e.New(e.CodeInternal, "container cleanup registry closed before %q could start", name)
}

var containerSequence atomic.Uint64

// Container names are execution-only. Identical producer IDs may execute for
// different outputs concurrently, so neither ID nor PID alone establishes ownership.
func containerName(n ir.Node) string {
	return fmt.Sprintf("nvkr-%d-%d-%s", os.Getpid(), containerSequence.Add(1), sanitizeName(n.ID))
}

func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
