package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func sessionSource(t *testing.T, root string) ir.Node {
	t.Helper()
	fetch := testFetchNode("fetch-shared", "https://example.test/source", testSourceCommit,
		testSourceTree, "shared", "registry.example/toolchain@sha256:"+repeatHex("c"), "")
	dir := filepath.Join(root, fetch.Dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("pinned input\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := sourceTreeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, fetch.Dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceReceipt(receiptPath, sourceReceipt{Schema: sourceReceiptSchema,
		Commit: fetch.Commit, TreeHash: fetch.TreeHash, VendorSpecDigest: fetch.VendorSpecDigest,
		ClosureDigest: digest}); err != nil {
		t.Fatal(err)
	}
	return fetch
}

func sessionRuntime(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime")
	// Record the mounted snapshot and runtime container name in the observed
	// artifact. This exercises dispatch and collection without a container daemon.
	script := `#!/bin/sh
set -eu
name=
source=
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --name) name=$2; shift 2 ;;
    -v)
      case "$2" in
        *:/src:ro) source=${2%:/src:ro} ;;
        *:/src/out) out=${2%:/src/out} ;;
      esac
      shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$name" ] && [ -n "$source" ] && [ -n "$out" ]
component=${name##*build-}
printf '%s\n%s\n' "$source" "$name" > "$out/$component"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNodeSessionSharesSnapshotsOnlyWithinSourceRoot(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "first", "src")
	fetch := sessionSource(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	runtime := sessionRuntime(t)
	execute := func(sourceRoot, component string) (*ArtifactResult, string, string) {
		t.Helper()
		node := completeTestBuild(ir.Node{ID: "build-" + component, Kind: ir.KindBuild, Component: component}, fetch)
		result, err := session.ExecNode(node, RunOptions{Ctx: t.Context(), SourceRoot: sourceRoot,
			Runtime: runtime, Launch: true, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(result.AbsPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != 2 {
			t.Fatalf("runtime observation = %q", raw)
		}
		return result, lines[0], lines[1]
	}
	a, firstSnapshot, firstName := execute(root, "a")
	b, reusedSnapshot, _ := execute(root, "b")
	if firstSnapshot != reusedSnapshot || a.InputDigest != b.InputDigest {
		t.Fatal("same-root consumers did not share the exact verified snapshot")
	}
	otherRoot := filepath.Join(work, "second", "src")
	sessionSource(t, otherRoot)
	c, siblingSnapshot, siblingName := execute(otherRoot, "a")
	if siblingSnapshot == firstSnapshot || c.InputDigest != a.InputDigest || siblingName == firstName {
		t.Fatal("independent output roots shared snapshot or container ownership")
	}
	dirtyRoot := filepath.Join(work, "dirty", "src")
	sessionSource(t, dirtyRoot)
	if err := os.WriteFile(filepath.Join(dirtyRoot, fetch.Dest, "input.txt"), []byte("changed after fetch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := completeTestBuild(ir.Node{ID: "build-a", Kind: ir.KindBuild, Component: "a"}, fetch)
	_, err = session.ExecNode(node, RunOptions{Ctx: t.Context(), SourceRoot: dirtyRoot, Runtime: runtime, Launch: true})
	if code(err) != e.CodeBuildFailed {
		t.Fatalf("dirty sibling inherited another root's snapshot: %v", err)
	}
	copy := *session
	copy.Close()
	for _, path := range []string{firstSnapshot, siblingSnapshot} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("closed session retained snapshot %s: %v", path, err)
		}
	}
	_, err = session.ExecNode(node, RunOptions{Ctx: t.Context(), Launch: true, DryRun: true})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("copied handle failed to revoke original: %v", err)
	}
}

type sessionBlockedWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	stop    sync.Once
}

func (writer *sessionBlockedWriter) Write(raw []byte) (int, error) {
	if strings.Contains(string(raw), "=== build ") {
		writer.once.Do(func() { close(writer.started) })
		<-writer.release
	}
	return len(raw), nil
}

func (writer *sessionBlockedWriter) unblock() { writer.stop.Do(func() { close(writer.release) }) }

func awaitSession(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestNodeSessionCloseCancelsJoinsAndRevokesCopies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	fetch := sessionSource(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	copy := *session
	writer := &sessionBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(writer.unblock)
	node := completeTestBuild(ir.Node{ID: "build-a", Kind: ir.KindBuild, Component: "a"}, fetch)
	runDone := make(chan error, 1)
	go func() {
		_, err := copy.ExecNode(node, RunOptions{Ctx: t.Context(), SourceRoot: root, Runtime: "must-not-run",
			Launch: true, Stdout: writer, Stderr: io.Discard})
		runDone <- err
	}()
	awaitSession(t, writer.started, "materialized build")
	closed := make(chan struct{})
	go func() { session.Close(); close(closed) }()
	awaitSession(t, session.state.ctx.Done(), "session cancellation")
	copyClosed := make(chan struct{})
	go func() { copy.Close(); close(copyClosed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a producer still held its snapshot")
	case <-copyClosed:
		t.Fatal("copied Close returned while a producer still held its snapshot")
	default:
	}
	snapshotRoot := filepath.Join(filepath.Dir(root), "snapshots")
	entries, err := os.ReadDir(snapshotRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("active snapshot was released before its producer joined: %v, %v", entries, err)
	}
	writer.unblock()
	awaitSession(t, closed, "Close")
	awaitSession(t, copyClosed, "copied Close")
	if err := <-runDone; code(err) != e.CodeTimeout {
		t.Fatalf("closed producer error = %v, want %s", err, e.CodeTimeout)
	}
	if entries, err := os.ReadDir(snapshotRoot); err != nil || len(entries) != 0 {
		t.Fatalf("joined session retained snapshots: %v, %v", entries, err)
	}
	_, err = copy.ExecNode(node, RunOptions{Ctx: t.Context(), Launch: true, DryRun: true})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("closed copy remained usable: %v", err)
	}
}

func TestNodeSessionRejectsUnavailableExecutionBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*NodeSession, *RunOptions)
		want   e.Code
	}{
		{name: "zero handle", change: func(s *NodeSession, _ *RunOptions) { *s = NodeSession{} }, want: e.CodeEngineCapability},
		{name: "missing context", change: func(_ *NodeSession, o *RunOptions) { o.Ctx = nil }, want: e.CodeUsage},
		{name: "direct execution", change: func(_ *NodeSession, o *RunOptions) { o.Launch = false }, want: e.CodeEngineCapability},
		{name: "qualified execution", change: func(_ *NodeSession, o *RunOptions) { o.Qualification = &QualifiedExecution{} }, want: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, err := NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			handle := *session
			root := filepath.Join(t.TempDir(), "absent")
			options := RunOptions{Ctx: t.Context(), Launch: true, SourceRoot: root}
			test.change(&handle, &options)
			_, err = handle.ExecNode(ir.Node{}, options)
			if code(err) != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("rejected session created output: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewNodeSession(ctx); code(err) != e.CodeTimeout {
		t.Fatalf("canceled owner received session: %v", err)
	}
}

func TestContainerNamesSeparateRepeatedBuildAndVendorUses(t *testing.T) {
	const uses = 32
	names := make(chan string, uses*2)
	var group sync.WaitGroup
	for _, id := range []string{"build-shared", "vendor-shared"} {
		for range uses {
			group.Go(func() { names <- containerName(ir.Node{ID: id}) })
		}
	}
	group.Wait()
	close(names)
	seen := make(map[string]bool, uses*2)
	for name := range names {
		if seen[name] || !strings.HasPrefix(name, fmt.Sprintf("nkd-%d-", os.Getpid())) {
			t.Fatalf("container ownership collides or omits process identity: %q", name)
		}
		seen[name] = true
	}
}
