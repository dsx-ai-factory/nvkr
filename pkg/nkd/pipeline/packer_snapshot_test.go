package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPackerSnapshotBindsOriginalInode(t *testing.T) {
	raw := []byte("original collected bytes\n")
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &PackerArtifact{file: file, digest: FileDigest(raw), size: int64(len(raw))}
	t.Cleanup(func() { _ = artifact.Close() })
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substituted pathname"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := artifact.SnapshotHeldContext(t.Context(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.ReadAllContext(t.Context(), 1024)
	if err != nil || !bytes.Equal(got, raw) || snapshot.Digest() != FileDigest(raw) {
		t.Fatalf("held original = %q, %v", got, err)
	}
}

func TestPackerSnapshotQueueHonorsContext(t *testing.T) {
	for _, mode := range []string{"deadline", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			artifact := &PackerArtifact{}
			artifact.mu.Lock()
			defer artifact.mu.Unlock()
			parent, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			ctx := &snapshotQueueContext{Context: parent, waiting: make(chan struct{})}
			result := make(chan error, 1)
			go func() {
				held, err := artifact.SnapshotHeldContext(ctx, 1024)
				if held != nil {
					_ = held.Close()
				}
				result <- err
			}()
			// Initial admission only checks Err. Done is evaluated by the
			// queue's select while the mutex remains held, proving cancellation
			// cannot be satisfied by the already-canceled admission path.
			select {
			case <-ctx.waiting:
			case <-time.After(5 * time.Second):
				artifact.mu.Unlock()
				<-result
				artifact.mu.Lock()
				t.Fatal("snapshot did not reach the lock queue")
			}
			if err := ctx.Err(); err != nil {
				artifact.mu.Unlock()
				<-result
				artifact.mu.Lock()
				t.Fatalf("context expired before queue entry: %v", err)
			}
			wantCause := context.DeadlineExceeded
			if mode == "cancellation" {
				wantCause = context.Canceled
				cancel()
			}
			select {
			case err := <-result:
				if packerErrorCode(err) != e.CodeTimeout || !errors.Is(err, wantCause) {
					t.Fatalf("queued snapshot must fail with timeout: %v", err)
				}
			case <-time.After(5 * time.Second):
				// Release the lock and join before failing, so a regression does
				// not leave the test's snapshot goroutine behind.
				artifact.mu.Unlock()
				<-result
				artifact.mu.Lock()
				t.Fatal("queued snapshot ignored its canceled context")
			}
		})
	}
}

// snapshotQueueContext observes the existing context boundary, not a production
// hook. It delegates cancellation unchanged and signals only the first Done call.
type snapshotQueueContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *snapshotQueueContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestPackerSnapshotRejectsInvalidAndChangedSubjects(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"nil context", e.CodeUsage}, {"canceled", e.CodeTimeout}, {"nil artifact", e.CodeUsage},
		{"closed", e.CodeVerifyFailed}, {"over budget", e.CodeEngineCapability},
		{"changed size", e.CodeVerifyFailed}, {"changed bytes", e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte("collected bytes\n")
			path := filepath.Join(t.TempDir(), "payload")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			artifact := &PackerArtifact{file: file, digest: FileDigest(raw), size: int64(len(raw))}
			t.Cleanup(func() { _ = artifact.Close() })
			ctx, budget := t.Context(), int64(1024)
			switch test.name {
			case "nil context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil artifact":
				artifact = nil
			case "closed":
				if err := artifact.Close(); err != nil {
					t.Fatal(err)
				}
			case "over budget":
				budget = 1
			case "changed size":
				artifact.size++
			case "changed bytes":
				artifact.digest = FileDigest([]byte("different bytes"))
			}
			held, err := artifact.SnapshotHeldContext(ctx, budget)
			if held != nil || packerErrorCode(err) != test.code {
				t.Fatalf("rejected snapshot = %v, %v", held, err)
			}
		})
	}
}
