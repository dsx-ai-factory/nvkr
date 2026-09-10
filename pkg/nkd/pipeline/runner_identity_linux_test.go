//go:build linux

package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestMappedRunnerSnapshotIgnoresExecutablePathReplacement(t *testing.T) {
	before, cleanupBefore, err := snapshotMappedRunnerExecutable()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupBefore()
	want := digestHeldRunner(t, before)

	pathname, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(pathname), "."+filepath.Base(pathname)+".nkd-mapped-backup")
	if err := os.Rename(pathname, backup); err != nil {
		t.Skipf("test environment does not permit executable pathname replacement: %v", err)
	}
	replaced := true
	defer func() {
		if replaced {
			_ = os.Remove(pathname)
			_ = os.Rename(backup, pathname)
		}
	}()
	if err := os.WriteFile(pathname, []byte("replacement pathname bytes\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	pathnameDigest, _, err := sha256File(pathname)
	if err != nil {
		t.Fatal(err)
	}
	if pathnameDigest == want {
		t.Fatal("runner replacement fixture did not change pathname bytes")
	}

	after, cleanupAfter, err := snapshotMappedRunnerExecutable()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupAfter()
	if got := digestHeldRunner(t, after); got != want {
		t.Fatalf("mapped runner identity followed replaced pathname: got %s want %s", got, want)
	}

	if err := os.Remove(pathname); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, pathname); err != nil {
		t.Fatal(err)
	}
	replaced = false
}

func TestMappedRunnerSnapshotIsAnonymousAndImmutable(t *testing.T) {
	held, cleanup, err := snapshotMappedRunnerExecutable()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	file := held.OSFile()
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "memfd:nkd-qualified-runner") || strings.Contains(link, os.TempDir()) {
		t.Fatalf("mapped runner retained a named mutable snapshot: %q", link)
	}
	if _, err := file.WriteAt([]byte("attacker"), 0); err == nil {
		t.Fatal("mapped runner capability remained writable after sealing")
	}
	if err := held.AssertUnchanged(); err != nil {
		t.Fatalf("failed mutation changed mapped runner capability: %v", err)
	}
}

func digestHeldRunner(t *testing.T, file *heldfile.File) string {
	t.Helper()
	if err := file.AssertUnchanged(); err != nil {
		t.Fatal(err)
	}
	return file.Digest()
}
