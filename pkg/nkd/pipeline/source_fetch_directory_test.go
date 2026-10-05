package pipeline

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestSourceFetchHeldGitDoesNotInheritAmbientCredentials(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held Git child execution requires Linux")
	}
	checkout, err := holdSourceFetchDirectory(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	commands := t.TempDir()
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte("#!/bin/sh\nexec /usr/bin/env\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := commands + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "ACTIONS_RUNTIME_TOKEN", "REGISTRY_AUTH_FILE", "HTTPS_PROXY", "UNSELECTED_SECRET"} {
		t.Setenv(key, "ambient-secret")
	}
	for _, private := range []bool{false, true} {
		opts := RunOptions{Ctx: t.Context(), fetchDirectory: checkout}
		if private {
			opts.SourceGitEnvironment = []string{"PATH=" + path, "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "NKD_SOURCE_TOKEN=selected-test-token"}
		}
		output, err := sourceFetchGitOut(opts, ".", "status")
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(output, "\n")
		if strings.Contains(output, "ambient-secret") || !slices.Contains(fields, "GIT_CONFIG_GLOBAL=/dev/null") || !slices.Contains(fields, "GIT_CONFIG_NOSYSTEM=1") {
			t.Fatal("held Git child inherited ambient state or lacked its fixed baseline")
		}
		if slices.Contains(fields, "NKD_SOURCE_TOKEN=selected-test-token") != private {
			t.Fatal("selected source credential escaped its private child scope")
		}
	}
}

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
