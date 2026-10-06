package pipeline

import (
	"context"
	"os"
	"runtime"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
	defer workspace.Close()
	call, root, release, err := workspace.borrow(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- workspace.Close() }()
	<-call.Done()
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
	release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(); err == nil {
		t.Fatal("closed owner retained live directory")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
}

func canceledTestContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
