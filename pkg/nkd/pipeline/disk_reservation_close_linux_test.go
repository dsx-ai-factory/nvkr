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
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// This isolated regression exercises the actual held ELF runtime child. It is
// not a libguestfs, container-daemon or installed-content acceptance test.
func TestDiskRuntimeCloseOwnsIssuedReservation(t *testing.T) {
	t.Run("unused permit and shallow copy", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		container, _ := newReservedDiskInspectionFixture(t, ctx, "unchanged")
		raw := []byte("still-open exact sized subject")
		reservation, err := container.AdmitSubject(ctx, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		defer reservation.Close()
		subject, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "reservation-copy", MaxBytes: 128})
		if err != nil {
			t.Fatal(err)
		}
		defer subject.Close()
		copy := *reservation
		if err := container.Close(); err != nil {
			t.Fatal(err)
		}
		reclaimed := diskInspectionCapacity.TryLock()
		if reclaimed {
			diskInspectionCapacity.Unlock()
		}
		if reclaimed {
			t.Fatal("runtime Close released the still-open caller snapshot budget")
		}
		if _, err := copy.InspectHeld(t.Context(), subject); code(err) != e.CodeEngineCapability {
			t.Fatalf("closed runtime reservation copy not revoked: %v", err)
		}
		if err := subject.Close(); err != nil {
			t.Fatal(err)
		}
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		if err := copy.Close(); err != nil {
			t.Fatal(err)
		}
		if err := container.Close(); err != nil {
			t.Fatal(err)
		}
		if !diskInspectionCapacity.TryLock() {
			t.Fatal("repeated Close lost the one reclaimed permit")
		}
		diskInspectionCapacity.Unlock()
	})
	t.Run("active child cancellation joins before release", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		container, root := newReservedDiskInspectionFixture(t, ctx, "close-join")
		raw := []byte("held runtime Close subject")
		reservation, err := container.AdmitSubject(ctx, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		defer reservation.Close()
		subject, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "close-owned-child", MaxBytes: 128})
		if err != nil {
			t.Fatal(err)
		}
		defer subject.Close()
		scanDone := make(chan error, 1)
		go func() { _, err := reservation.InspectHeld(ctx, subject); scanDone <- err }()
		joined := false
		closeJoined := false
		closeStarted := false
		closeDone := make(chan error, 1)
		defer func() {
			cancel()
			_ = os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0600)
			if !joined {
				_ = waitCloseTestResult(t, scanDone, "deferred scan cleanup")
			}
			if closeStarted && !closeJoined {
				_ = waitCloseTestResult(t, closeDone, "deferred runtime Close")
			}
		}()
		waitDiskInspectionFixtureMarker(t, ctx, nil, root, "consumed")
		closeStarted = true
		go func() { closeDone <- container.Close() }()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "cleanup-started")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("runtime Close did not cancel consumed-input child into owned cleanup")
			}
			time.Sleep(time.Millisecond)
		}
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
		scanErr := waitCloseTestResult(t, scanDone, "scan")
		joined = true
		if code(scanErr) != e.CodeTimeout {
			t.Fatalf("Close returned without canceling its active scan: %v", scanErr)
		}
		closeErr := waitCloseTestResult(t, closeDone, "runtime Close")
		closeJoined = true
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if _, err := os.Stat(filepath.Join(root, "removed")); err != nil {
			t.Fatalf("Close returned before owned child cleanup: %v", err)
		}
		if diskInspectionCapacity.TryLock() {
			diskInspectionCapacity.Unlock()
			t.Fatal("joined runtime released still-open caller snapshot budget")
		}
		copy := *reservation
		if _, err := copy.InspectHeld(t.Context(), subject); code(err) != e.CodeEngineCapability {
			t.Fatalf("closed runtime reservation copy usable: %v", err)
		}
		if err := subject.Close(); err != nil {
			t.Fatal(err)
		}
		if err := copy.Close(); err != nil {
			t.Fatal(err)
		}
		if err := container.Close(); err != nil {
			t.Fatal(err)
		}
		if !diskInspectionCapacity.TryLock() {
			t.Fatal("closed caller snapshot and permit retained global capacity")
		}
		diskInspectionCapacity.Unlock()
	})
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
	first, err := container.AdmitSubject(ctx, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	type admission struct {
		permit *DiskInspectionReservation
		err    error
	}
	queued := make(chan admission, 1)
	go func() { permit, err := container.AdmitSubject(ctx, 128); queued <- admission{permit, err} }()
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
