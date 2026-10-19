// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type activityLockWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *activityLockWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return len(data), nil
}

type activityLaunchLockRuntime struct {
	heldPackerContainerRuntime
	blocked string
	writer  *activityLockWriter
	attach  chan struct{}
	cleanup chan struct{}
}

// Borrow the executable as an input to keep its real heldfile launch lock for
// the command's entire lifetime. The test-owned writer then delays Wait despite
// cancellation. This exercises the same Close lock contention as a blocked
// Start, without a heldfile test hook or a deliberately kernel-stuck process.
func (runtime *activityLaunchLockRuntime) lockedCommand(ctx context.Context) error {
	capability := runtime.runtime.capability
	return heldfile.RunContext(ctx, capability, nil, heldfile.RunOptions{
		Stdout: runtime.writer, Stderr: io.Discard, Env: []string{"PATH=/nonexistent"},
		TerminationGrace: time.Millisecond,
	}, capability)
}

func (runtime *activityLaunchLockRuntime) Run(ctx context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
	if args[0] == "create" {
		_, err := io.WriteString(stdout, "private-observer-id\n")
		return err
	}
	defer close(runtime.attach)
	if runtime.blocked == "attach" {
		return runtime.lockedCommand(ctx)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (runtime *activityLaunchLockRuntime) Cleanup(ctx context.Context, _ time.Duration, _, _ io.Writer, _ ...string) error {
	defer close(runtime.cleanup)
	if runtime.blocked == "cleanup" {
		return runtime.lockedCommand(ctx)
	}
	return nil
}

func TestPackerActivityUnjoinedRuntimeDoesNotBlockSessionClose(t *testing.T) {
	for _, blocked := range []string{"attach", "cleanup"} {
		t.Run(blocked, func(t *testing.T) {
			capability, err := heldfile.FromBytesContext(t.Context(), []byte("#!/bin/sh\nprintf activity\n"), heldfile.Options{
				Label: "activity-close-lock", Executable: true, MaxBytes: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = capability.Close() })
			configuration := t.TempDir()
			marker := filepath.Join(configuration, "retained-config")
			if err := os.WriteFile(marker, []byte("private runtime config"), 0o600); err != nil {
				t.Fatal(err)
			}
			held := &heldRuntimeExecutable{capability: capability, name: "docker", endpoint: qualifiedRuntimeEndpoint, configurationRoot: configuration}
			runtime := &activityLaunchLockRuntime{heldPackerContainerRuntime: heldPackerContainerRuntime{runtime: held}, blocked: blocked,
				writer: &activityLockWriter{started: make(chan struct{}), release: make(chan struct{})},
				attach: make(chan struct{}), cleanup: make(chan struct{})}
			handle, err := NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan struct{})
			close(ready)
			handle.state.packerRuntime = &nodeSessionPackerRuntime{runtime: held, ready: ready}
			sink, _ := activityTestSink(t)
			resources := &packerContainerResources{runtime: runtime, volumes: []string{"private-work"}}
			request := packerContainerRunRequest{runtime: runtime, activity: sink, activityOperation: "os/build-disk",
				bindings: packerContainerBindings{buildLogDirectoryVariable: "logs"},
				identity: packerContainerIdentity{passwdPath: "private-passwd", groupPath: "private-group"}}
			observer := startPackerActivity(t.Context(), request, resources, "private-work", "private-observer", nil)
			if observer == nil {
				t.Fatal("observer not admitted")
			}
			released := false
			defer func() {
				if !released {
					close(runtime.writer.release)
				}
				// Also stop on an early assertion failure. The test owns and
				// joins both clients before directly releasing its capability;
				// production schedules no cleanup after a failed stop.
				_ = observer.stopWithTimeout(t.Context(), time.Second)
				for _, done := range []chan struct{}{runtime.attach, runtime.cleanup} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("test-owned runtime command did not join after release")
					}
				}
			}()
			if blocked == "attach" {
				select {
				case <-runtime.writer.started:
				case <-time.After(time.Second):
					t.Fatal("held runtime command did not acquire its launch lock")
				}
			}
			if err := observer.stopWithTimeout(t.Context(), 50*time.Millisecond); code(err) != e.CodeTimeout {
				t.Fatal("unjoined client did not retain its runtime", err)
			}
			select {
			case <-runtime.writer.started:
			default:
				t.Fatal("blocked command did not hold the executable lock")
			}
			closed := make(chan struct{})
			go func() { handle.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				// Release before failing, so a regression joins its Close too.
				close(runtime.writer.release)
				released = true
				<-closed
				t.Fatal("session Close blocked on an unjoined observer executable")
			}
			if capability.OSFile() == nil || code(held.close()) != e.CodeTimeout || code(resources.cleanup()) != e.CodeTimeout {
				t.Fatal("session or standalone cleanup released retained dependencies")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("unjoined runtime configuration was released", err)
			}
			close(runtime.writer.release)
			released = true
			<-runtime.attach
			<-runtime.cleanup
			if code(held.close()) != e.CodeTimeout || capability.OSFile() == nil || code(resources.cleanup()) != e.CodeTimeout {
				t.Fatal("late join silently authorized runtime release")
			}
		})
	}
}

func TestPackerActivityOrdinaryRuntimeCloseStillReleases(t *testing.T) {
	capability, err := heldfile.FromBytesContext(t.Context(), []byte("#!/bin/sh\nexit 0\n"), heldfile.Options{
		Label: "activity-ordinary-close", Executable: true, MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	held := &heldRuntimeExecutable{capability: capability, name: "docker"}
	if err := held.close(); err != nil || capability.OSFile() != nil {
		t.Fatal("ordinary runtime Close changed", err)
	}
}
