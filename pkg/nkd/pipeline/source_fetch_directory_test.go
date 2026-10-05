package pipeline

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestSourceFetchHeldDirectoryAndReceiptSurviveParentSubstitution(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held Git directory execution is Linux-only")
	}
	node, dir, opts := pristineClosureFixture(t)
	checkout, err := holdSourceFetchDirectory(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	opts.fetchDirectory = checkout
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := holdSourceFetchDirectory(t.Context(), filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	opts.fetchReceiptDirectory = receipts
	parent := filepath.Dir(opts.SourceRoot)
	heldParent := parent + "-held"
	outside := t.TempDir()
	if err := os.Rename(parent, heldParent); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(parent)
		_ = os.Rename(heldParent, parent)
	}()
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceFetchGitOut(opts, dir, "config", "nkd.held", "true"); err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Logf("controlled Git diagnostic: %s", failure.Stderr)
		}
		t.Fatal(err)
	}
	if err := ensureSourceClosure(node, dir, opts, nil); err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path, receipts)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("held receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("Git or receipt wrote through replacement: %v / %v", entries, err)
	}
}

func TestSourceFetchHeldWorkspacePlatformAdmissionBeforeWork(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported-platform admission is exercised on other hosts")
	}
	_, repository, _ := pristineClosureFixture(t)
	t.Chdir(t.TempDir())
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	err = (Local{}).fetch(ir.Node{Dest: "source", Repo: repository}, RunOptions{
		Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace,
	})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("unsupported held Git route: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported platform created output: %v / %v", entries, err)
	}
}

func TestSourceFetchHeldWorkspaceRealLocalGit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held Git directory execution is Linux-only")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	t.Chdir(t.TempDir())
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace, Stdout: io.Discard, Stderr: io.Discard})
	for range 2 {
		if err := (Local{}).fetch(node, opts); err != nil {
			t.Fatal(err)
		}
	}
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("held fetch receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
}

func TestSourceFetchRealLocalGitCreatesAndReusesReceipt(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("inherited directory paths are capability-gated on this platform")
	}
	node, repository, _ := pristineClosureFixture(t)
	node.Repo = repository
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(t.TempDir(), "src"), Stdout: io.Discard, Stderr: io.Discard})
	for range 2 {
		if err := (Local{}).fetch(node, opts); err != nil {
			t.Fatal(err)
		}
	}
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("real fetch receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
}
