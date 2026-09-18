package pipeline

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func pristineClosureFixture(t *testing.T) (ir.Node, string, RunOptions) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "src")
	dir := filepath.Join(root, "source")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"input": "source bytes", ".gitignore": "ignored\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s, %v", raw, err)
		}
	}
	commit, err := gitOut(t.Context(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitOut(t.Context(), dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	return ir.Node{ID: "fetch-source", Dest: "source", Commit: commit, TreeHash: tree}, dir, RunOptions{Ctx: t.Context(), SourceRoot: root}
}

func TestSourceClosureReceiptVendorsOnceAndRejectsDrift(t *testing.T) {
	node, dir, opts := pristineClosureFixture(t)
	node.VendorCmd = []string{"vendor"}
	node.VendorImage = "registry.example/tool@sha256:" + repeatHex("a")
	calls := 0
	vendor := func() error {
		calls++
		return os.WriteFile(filepath.Join(dir, "vendored"), []byte("pinned dependencies"), 0o644)
	}
	for i := 0; i < 2; i++ {
		if err := ensureSourceClosure(node, dir, opts, vendor); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("vendored %d times", calls)
	}
	path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), path)
	if err != nil || !exists || receipt.Commit != node.Commit || receipt.TreeHash != node.TreeHash {
		t.Fatalf("receipt lost source binding: %+v, %v", receipt, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendored"), []byte("changed dependencies"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSourceClosure(node, dir, opts, vendor); code(err) != e.CodeFetchFailed {
		t.Fatalf("drift accepted: %v", err)
	}
	if calls != 1 {
		t.Fatal("drift silently re-vendored source")
	}
}

func TestSourceClosureRejectsUnreceiptedChangesAndVendorFailures(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "ignored", "missing vendor", "failed vendor"} {
		t.Run(kind, func(t *testing.T) {
			node, dir, opts := pristineClosureFixture(t)
			var vendor func() error
			var expected error
			switch kind {
			case "tracked", "untracked", "ignored":
				name := kind
				if kind == "tracked" {
					name = "input"
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("unreceipted bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			default:
				node.VendorCmd = []string{"vendor"}
				node.VendorImage = "registry.example/tool@sha256:" + repeatHex("a")
				if kind == "failed vendor" {
					expected = errors.New("vendor failed")
					vendor = func() error { return expected }
				}
			}
			err := ensureSourceClosure(node, dir, opts, vendor)
			if expected != nil {
				if !errors.Is(err, expected) {
					t.Fatalf("vendor failure lost: %v", err)
				}
			} else if code(err) != e.CodeFetchFailed {
				t.Fatalf("dirty source accepted: %v", err)
			}
			path, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failed source issued receipt: %v", err)
			}
		})
	}
}
