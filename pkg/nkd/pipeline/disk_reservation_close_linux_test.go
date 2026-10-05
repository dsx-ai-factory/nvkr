//go:build linux

package pipeline

import (
	"context"
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
	if testing.Short() {
		t.Skip("real queue wait exceeds the former 30-second ceiling")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	container, _ := newReservedDiskInspectionFixture(t, ctx, "unchanged")
	container.selection.TimeoutSeconds = 40
	first, err := container.reserveSubject(ctx, 128, true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	type admission struct {
		permit *diskInspectionReservation
		err    error
	}
	queued := make(chan admission, 1)
	go func() { permit, err := container.reserveSubject(ctx, 128, true); queued <- admission{permit, err} }()
	joined := false
	defer func() {
		cancel()
		_ = first.Close()
		if joined {
			return
		}
		select {
		case result := <-queued:
			if result.permit != nil {
				_ = result.permit.Close()
			}
		case <-time.After(5 * time.Second):
			t.Error("canceled capacity queue did not join")
		}
	}()
	timer := time.NewTimer(31 * time.Second)
	defer timer.Stop()
	select {
	case result := <-queued:
		joined = true
		if result.permit != nil {
			_ = result.permit.Close()
		}
		t.Fatalf("declared 40-second queue ended before former ceiling: %v", result.err)
	case <-timer.C:
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-queued:
		joined = true
		if result.err != nil {
			t.Fatalf("queued admission after31seconds: %v", result.err)
		}
		if err := result.permit.Close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued admission failed to join")
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
