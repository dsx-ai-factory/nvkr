//go:build linux || darwin

package status

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func writeStatusStateFile(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusHighWaterCompareAndSwapRejectsStaleWriter(t *testing.T) {
	path := writeStatusStateFile(t, "high-water.json", []byte(`{"current":2}`))
	err := persistStatusHighWaterContext(context.Background(), path, []byte(`{"current":1}`), []byte(`{"current":3}`))
	if code(err) != nkderr.CodeWriteConflict {
		t.Fatalf("code = %q, want %q (%v)", code(err), nkderr.CodeWriteConflict, err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != `{"current":2}` {
		t.Fatalf("stale writer changed high-water: %s", raw)
	}
}

func TestStatusHighWaterRejectsSymlinkAtBothReadBoundaries(t *testing.T) {
	dir := t.TempDir()
	referent := filepath.Join(dir, "referent.json")
	initial := []byte(`{"current":1}`)
	if err := os.WriteFile(referent, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "high-water.json")
	if err := os.Symlink(referent, path); err != nil {
		t.Fatal(err)
	}
	var decoded highWaterFile
	if _, err := readProtectedHighWaterContext(context.Background(), path, &decoded); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("initial symlink read code = %q, want %q (%v)",
			code(err), nkderr.CodeStatusCheckpoint, err)
	}
	if err := persistStatusHighWaterContext(context.Background(), path, initial, []byte(`{"current":2}`)); code(err) != nkderr.CodeWriteConflict {
		t.Fatalf("under-lock symlink read code = %q, want %q (%v)",
			code(err), nkderr.CodeWriteConflict, err)
	}
	got, err := os.ReadFile(referent)
	if err != nil || string(got) != string(initial) {
		t.Fatalf("symlink race changed referent: %s (%v)", got, err)
	}
}

func TestStatusStateIORejectsCancellationAndFIFOs(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "must-not-exist")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var decoded highWaterFile
	if _, err := readProtectedHighWaterContext(ctx, missing, &decoded); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled high-water read error = %v, want retryable timeout", err)
	}
	if err := persistStatusHighWaterContext(ctx, missing, nil, []byte(`{"next":1}`)); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled high-water write error = %v, want retryable timeout", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled status I/O touched its target: %v", err)
	}

	fifo := filepath.Join(root, "attacker-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedHighWaterContext(context.Background(), fifo, &decoded); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("FIFO high-water error = %v, want status-checkpoint", err)
	}
	if _, err := acquireStatusHighWaterLockContext(context.Background(), fifo); code(err) != nkderr.CodeWriteConflict {
		t.Fatalf("FIFO lease error = %v, want write-conflict", err)
	}
}

func TestStatusHighWaterRecoversWhenLeaseOwnerDies(t *testing.T) {
	path := writeStatusStateFile(t, "high-water.json", []byte(`{"current":1}`))
	leasePath := path + ".lease"
	command := exec.Command(os.Args[0], "-test.run=^TestStatusHighWaterCrashLeaseHelper$")
	command.Env = append(os.Environ(), "NKD_STATUS_CRASH_LEASE_HELPER="+leasePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash lease helper: %v: %s", err, output)
	}

	next := []byte(`{"current":2}`)
	if err := persistStatusHighWaterContext(context.Background(), path, []byte(`{"current":1}`), next); err != nil {
		t.Fatalf("kernel-released crash-stale lock prevented recovery: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(next) {
		t.Fatalf("recovered high-water = %s, want %s", got, next)
	}
	if info, err := os.Lstat(leasePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("reusable advisory lock inode was removed or replaced: %v (%v)", info, err)
	}
}

func TestStatusHighWaterCrashLeaseHelper(t *testing.T) {
	leasePath := os.Getenv("NKD_STATUS_CRASH_LEASE_HELPER")
	if leasePath == "" {
		return
	}
	lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass defer/Close/LOCK_UN. Process exit is the recovery event
	// and must leave only an unlocked, safely reusable directory entry.
	os.Exit(0)
}

func TestStatusHighWaterRejectsConcurrentLockOwner(t *testing.T) {
	path := writeStatusStateFile(t, "high-water.json", []byte(`{"current":1}`))
	lease, err := os.OpenFile(path+".lease", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lease.Fd()), unix.LOCK_UN)

	err = persistStatusHighWaterContext(context.Background(), path, []byte(`{"current":1}`), []byte(`{"current":2}`))
	if code(err) != nkderr.CodeWriteConflict {
		t.Fatalf("active cross-process lock code = %q, want %q (%v)",
			code(err), nkderr.CodeWriteConflict, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != `{"current":1}` {
		t.Fatalf("contending writer changed high-water: %s (%v)", got, readErr)
	}
}
