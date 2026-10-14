// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

// A controlled ELF reports fixture bytes: no real Debian inventory or daemon.
func retainedDebianInspector(t *testing.T, ctx context.Context, mode string) (*DiskInspectionContainer, string) {
	t.Helper()
	container, root := newReservedDiskInspectionFixture(t, ctx, mode)
	container.selection.GuestfishDigest = "sha256:" + strings.Repeat("d", 64)
	container.selection.ScannerDigest = "sha256:" + strings.Repeat("e", 64)
	container.selection.PackageReaderDigest = "sha256:" + strings.Repeat("f", 64)
	container.selection.MaxFilesystemBytes = 1 << 20
	container.selection.ScratchStorage = "memory"
	container.selection.Resources.MemoryMiB = 8192
	container.selection.Resources.EphemeralStorageMiB = 8192
	container.observeHost = func(context.Context, *os.File) (execution.ObservedHostFacts, error) {
		return execution.ObservedHostFacts{Platform: execution.HostPlatform(container.selection.Platform), Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 9216, EphemeralStorageMiB: 8192}}, nil
	}
	return container, root
}

func TestDebianProducerContentOwnsOriginalBytesAndTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, archive, node, _, path := capturedArchiveFixture(t)
	container, root := retainedDebianInspector(t, ctx, "unchanged")
	want := node.Debian.Platform
	node.Debian.Platform = "linux/arm64"
	node.Debian.Distribution = "different"
	if err := os.WriteFile(path, []byte("caller replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := container.InspectDebianProducerContent(ctx, archive)
	if err != nil || got == nil {
		t.Fatal("retained inspection failed", err)
	}
	digest, size, _, _, _ := got.Binding()
	kind, _, _, _, platform, _ := got.ContentBinding()
	if digest != archive.lease.digest || size != archive.lease.size || kind != "host-package" || platform != want {
		t.Fatal("producer binding substituted")
	}
	if _, err := os.Stat(filepath.Join(root, "removed")); err != nil {
		t.Fatal("inspection returned before cleanup", err)
	}
	if _, err := archive.WriteToContext(ctx, io.Discard); err != nil {
		t.Fatal("inspection closed Run bytes", err)
	}
}

func TestDebianProducerContentCloseJoinsInspectionCleanup(t *testing.T) {
	for _, closer := range []string{"session", "runtime"} {
		t.Run(closer, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			owner, archive, _, _, _ := capturedArchiveFixture(t)
			session, err := NewNodeSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			session.state.debianArchives = owner
			t.Cleanup(session.Close)
			container, root := retainedDebianInspector(t, ctx, "close-join")
			scanDone := make(chan error, 1)
			go func() {
				got, err := container.InspectDebianProducerContent(ctx, archive)
				if got != nil {
					scanDone <- e.New(e.CodeInternal, "canceled inspection returned observation")
					return
				}
				scanDone <- err
			}()
			closeDone := make(chan error, 1)
			scanJoined, closeJoined, closeStarted := false, false, false
			defer func() {
				cancel()
				_ = os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0o600)
				if !scanJoined {
					_ = waitCloseTestResult(t, scanDone, "deferred retained scan")
				}
				if closeStarted && !closeJoined {
					_ = waitCloseTestResult(t, closeDone, "deferred retained Close")
				}
			}()
			waitDiskInspectionFixtureMarker(t, ctx, nil, root, "consumed")
			closeStarted = true
			if closer == "session" {
				go func() { closeDone <- session.closeOwned() }()
			} else {
				go func() { closeDone <- container.Close() }()
			}
			waitDiskInspectionFixtureMarker(t, ctx, nil, root, "cleanup-started")
			select {
			case err := <-closeDone:
				closeJoined = true
				t.Fatalf("Close returned before cleanup: %v", err)
			default:
			}
			if diskInspectionCapacity.TryLock() {
				diskInspectionCapacity.Unlock()
				t.Fatal("inspection permit released before cleanup")
			}
			if err := os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			scanErr := waitCloseTestResult(t, scanDone, "retained scan")
			scanJoined = true
			if code(scanErr) != e.CodeTimeout {
				t.Fatal(scanErr)
			}
			closeErr := waitCloseTestResult(t, closeDone, "retained Close")
			closeJoined = true
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if closer == "session" {
				if _, err := archive.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
					t.Fatal("session close left authority", err)
				}
			} else {
				if _, err := archive.WriteToContext(t.Context(), io.Discard); err != nil {
					t.Fatal("runtime close released Run bytes", err)
				}
			}
			if !diskInspectionCapacity.TryLock() {
				t.Fatal("inspection permit leaked")
			}
			diskInspectionCapacity.Unlock()
		})
	}
}
