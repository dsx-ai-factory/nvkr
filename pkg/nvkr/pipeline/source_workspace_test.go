// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceWorkspaceAdmissionBeforeFilesystemWork(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil", nil, e.CodeUsage},
		{"canceled", canceledTestContext(), e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			workspace, err := HoldSourceWorkspace(test.ctx)
			if workspace != nil || code(err) != test.code {
				t.Fatalf("invalid owner admitted: %v, %v", workspace, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("owner admission wrote output: %v, %v", entries, err)
			}
		})
	}
}

func TestSourceWorkspaceZeroValueCannotLaunchFetch(t *testing.T) {
	t.Chdir(t.TempDir())
	node := testFetchNode("fetch-source", "https://example.invalid/source", testSourceCommit, testSourceTree,
		"source", "registry.example/toolchain@sha256:"+repeatHex("c"), "")
	_, err := (Local{}).ExecNode(node, RunOptions{Ctx: t.Context(), SourceRoot: "src",
		SourceWorkspace: &SourceWorkspace{}, Launch: true})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("unissued workspace launched fetch: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("unissued workspace wrote output: %v, %v", entries, err)
	}
}

func TestSourceWorkspaceCloseCancelsJoinsAndRevokes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued source workspace requires Linux")
	}
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	call, root, release, err := workspace.borrow(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var released sync.Once
	join := func() { released.Do(release) }
	// Release must run before Close cleanup even when a later assertion fails.
	t.Cleanup(join)
	closed := make(chan error, 2)
	go func() { closed <- workspace.Close() }()
	awaitSourceWorkspaceCancellation(t, call)
	go func() { closed <- workspace.Close() }()
	if _, err := root.Stat(); err != nil {
		t.Fatalf("owner released directory before fetch join: %v", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before fetch join: %v", err)
	default:
	}
	if _, _, _, err := workspace.borrow(t.Context()); code(err) != e.CodeEngineCapability {
		t.Fatalf("closing workspace admitted another call: %v", err)
	}
	join()
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Close did not join the released fetch")
		}
	}
	if _, err := root.Stat(); err == nil {
		t.Fatal("closed owner retained live directory")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceWorkspaceBorrowCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued source workspace requires Linux")
	}
	for _, test := range []struct {
		name                          string
		ownerCanceled, callerCanceled bool
		code                          e.Code
	}{
		{"owner canceled", true, false, e.CodeTimeout},
		{"caller canceled", false, true, e.CodeTimeout},
		{"nil caller", false, false, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			owner, cancelOwner := context.WithCancel(t.Context())
			t.Cleanup(cancelOwner)
			workspace, err := HoldSourceWorkspace(owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workspace.Close() })
			caller, cancelCaller := context.WithCancel(t.Context())
			t.Cleanup(cancelCaller)
			if test.ownerCanceled {
				cancelOwner()
			} else if test.callerCanceled {
				cancelCaller()
			} else {
				caller = nil
			}
			call, root, release, err := workspace.borrow(caller)
			if release != nil {
				t.Cleanup(release)
			}
			if code(err) != test.code || call != nil || root != nil || release != nil {
				t.Fatalf("canceled borrow admitted work: context=%v root=%v release=%t error=%v", call, root, release != nil, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("canceled borrow wrote output: %v, %v", entries, err)
			}
		})
	}
}

func TestSourceWorkspaceParentCancellationReachesBorrow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued source workspace requires Linux")
	}
	for _, parent := range []string{"owner", "caller"} {
		t.Run(parent, func(t *testing.T) {
			t.Chdir(t.TempDir())
			owner, cancelOwner := context.WithCancel(t.Context())
			t.Cleanup(cancelOwner)
			workspace, err := HoldSourceWorkspace(owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workspace.Close() })
			caller, cancelCaller := context.WithCancel(t.Context())
			t.Cleanup(cancelCaller)
			call, root, release, err := workspace.borrow(caller)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			if parent == "owner" {
				cancelOwner()
			} else {
				cancelCaller()
			}
			awaitSourceWorkspaceCancellation(t, call)
			if _, err := root.Stat(); err != nil {
				t.Fatalf("parent cancellation released the directory before fetch join: %v", err)
			}
		})
	}
}

func TestSourceWorkspaceCloseSharesCodedRootError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued source workspace requires Linux")
	}
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	if err := workspace.state.root.Close(); err != nil {
		t.Fatal(err)
	}
	first := workspace.Close()
	if code(first) != e.CodeFetchFailed {
		t.Fatalf("root Close error lacks its owning code: %v", first)
	}
	if repeated := workspace.Close(); repeated != first {
		t.Fatalf("repeated Close did not share original error: first=%v repeated=%v", first, repeated)
	}
}

func awaitSourceWorkspaceCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("source workspace did not cancel its active fetch")
	}
}

func canceledTestContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
