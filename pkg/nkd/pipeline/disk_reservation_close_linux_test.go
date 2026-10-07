// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This isolated regression exercises the actual held ELF runtime child. It is
// not a libguestfs, container-daemon or installed-content acceptance test.
func TestDiskRuntimeCloseOwnsPackerInspection(t *testing.T) {
	for _, phase := range []string{"snapshot queue", "active child cleanup"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			mode := "unchanged"
			if phase == "active child cleanup" {
				mode = "close-join"
			}
			container, root := newReservedDiskInspectionFixture(t, ctx, mode)
			raw := []byte("held runtime Close subject")
			path := filepath.Join(t.TempDir(), "payload")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			artifact := &PackerArtifact{file: file, size: int64(len(raw)), digest: FileDigest(raw)}
			defer artifact.Close()
			locked := phase == "snapshot queue"
			if locked {
				artifact.mu.Lock()
			}
			scanDone := make(chan error, 1)
			go func() { _, err := container.InspectPackerArtifact(ctx, artifact); scanDone <- err }()
			scanJoined, closeJoined, closeStarted := false, false, false
			closeDone := make(chan error, 1)
			defer func() {
				cancel()
				if locked {
					artifact.mu.Unlock()
				}
				_ = os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0600)
				if !scanJoined {
					_ = waitCloseTestResult(t, scanDone, "deferred scan cleanup")
				}
				if closeStarted && !closeJoined {
					_ = waitCloseTestResult(t, closeDone, "deferred runtime Close")
				}
			}()
			if phase == "snapshot queue" {
				deadline := time.Now().Add(3 * time.Second)
				for diskInspectionCapacity.TryLock() {
					diskInspectionCapacity.Unlock()
					if time.Now().After(deadline) {
						t.Fatal("owned operation never reserved before snapshot queue")
					}
					time.Sleep(time.Millisecond)
				}
			} else {
				waitDiskInspectionFixtureMarker(t, ctx, nil, root, "consumed")
			}
			closeStarted = true
			go func() { closeDone <- container.Close() }()
			if phase == "active child cleanup" {
				waitDiskInspectionFixtureMarker(t, ctx, nil, root, "cleanup-started")
				select {
				case err := <-closeDone:
					closeJoined = true
					t.Fatalf("Close returned before blocked cleanup joined: %v", err)
				default:
				}
				if diskInspectionCapacity.TryLock() {
					diskInspectionCapacity.Unlock()
					t.Fatal("capacity released while owned cleanup was blocked")
				}
				if err := os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			scanErr := waitCloseTestResult(t, scanDone, "scan")
			scanJoined = true
			if code(scanErr) != e.CodeTimeout {
				t.Fatalf("Close did not cancel owned operation: %v", scanErr)
			}
			closeErr := waitCloseTestResult(t, closeDone, "runtime Close")
			closeJoined = true
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if locked {
				artifact.mu.Unlock()
				locked = false
			}
			if phase == "active child cleanup" {
				if _, err := os.Stat(filepath.Join(root, "removed")); err != nil {
					t.Fatalf("Close returned before child cleanup: %v", err)
				}
			}
			if !diskInspectionCapacity.TryLock() {
				t.Fatal("owned snapshot lifetime leaked capacity after Close")
			}
			diskInspectionCapacity.Unlock()
			if _, err := container.InspectPackerArtifact(t.Context(), artifact); code(err) != e.CodeEngineCapability {
				t.Fatalf("closed runtime accepted a new operation: %v", err)
			}
			if err := container.Close(); err != nil {
				t.Fatal(err)
			}
			if !diskInspectionCapacity.TryLock() {
				t.Fatal("idempotent Close lost released capacity")
			}
			diskInspectionCapacity.Unlock()
		})
	}
}

func TestDiskRuntimeCloseOperationRegistrationRace(t *testing.T) {
	container := &DiskInspectionContainer{}
	ctx, finish, err := container.beginOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			finish()
		}
	}()
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			operation, done, err := container.beginOperation(t.Context())
			if err != nil {
				if code(err) != e.CodeEngineCapability {
					t.Errorf("registration error: %v", err)
				}
				return
			}
			defer done()
			select {
			case <-operation.Done():
			case <-time.After(5 * time.Second):
				t.Error("registered operation was not stopped")
			}
		})
	}
	closed := make(chan error, 1)
	go func() { closed <- container.Close() }()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop registered operation")
	}
	finish()
	finished = true
	workers.Wait()
	if err := waitCloseTestResult(t, closed, "concurrent Close"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := container.beginOperation(t.Context()); code(err) != e.CodeEngineCapability {
		t.Fatalf("post-Close registration: %v", err)
	}
}

func TestDiskRuntimeCapacityQueueUsesDeclaredTimeout(t *testing.T) {
	for _, callerTimeout := range []time.Duration{time.Minute, 5 * time.Second} {
		t.Run(callerTimeout.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), callerTimeout)
			defer cancel()
			callerDeadline, _ := ctx.Deadline()
			container := &DiskInspectionContainer{selection: DiskInspectionSelection{TimeoutSeconds: 40}}
			waiting := make(chan context.Context, 1)
			container.waitForCapacity = func(queued context.Context) error {
				waiting <- queued
				return lockDiskInspection(queued, &diskInspectionCapacity)
			}
			diskInspectionCapacity.Lock()
			defer diskInspectionCapacity.Unlock()
			done := make(chan error, 1)
			started := time.Now()
			go func() {
				permit, err := container.reserveSubject(ctx, 128, true)
				if permit != nil {
					_ = permit.Close()
				}
				done <- err
			}()
			joined := false
			defer func() {
				cancel()
				if !joined {
					_ = waitCloseTestResult(t, done, "deferred capacity queue")
				}
			}()
			select {
			case queued := <-waiting:
				deadline, ok := queued.Deadline()
				if !ok {
					t.Fatal("capacity queue has no deadline")
				}
				if callerDeadline.Before(started.Add(40 * time.Second)) {
					if !deadline.Equal(callerDeadline) {
						t.Fatalf("shorter caller deadline changed: %s, want %s", deadline, callerDeadline)
					}
				} else if deadline.Before(started.Add(40*time.Second)) || deadline.After(time.Now().Add(40*time.Second)) {
					t.Fatalf("capacity queue does not use declared 40-second timeout: %s", deadline)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admission did not reach the capacity queue")
			}
			cancel()
			err := waitCloseTestResult(t, done, "canceled capacity queue")
			joined = true
			if code(err) != e.CodeTimeout || !errors.Is(err, context.Canceled) {
				t.Fatalf("queued cancellation: %v", err)
			}
		})
	}
}

func waitCloseTestResult(t *testing.T, result <-chan error, phase string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not join its owned operation", phase)
		return nil
	}
}
