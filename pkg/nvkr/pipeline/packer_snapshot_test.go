// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
	snapshot, err := artifact.snapshotHeldContext(t.Context(), 1024)
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

// TeeReader invokes Write only after the real source has supplied bytes. This
// makes mid-copy cancellation independent of the number of context checks.
type snapshotCopyCancelWriter struct {
	cancel context.CancelFunc
	bytes  int
}

func (writer *snapshotCopyCancelWriter) Write(raw []byte) (int, error) {
	writer.bytes += len(raw)
	writer.cancel()
	return len(raw), nil
}

func TestPackerSnapshotCancellationDuringCopy(t *testing.T) {
	raw := bytes.Repeat([]byte("snapshot original bytes\n"), 100000)
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &PackerArtifact{file: file, digest: FileDigest(raw), size: int64(len(raw))}
	defer artifact.Close()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &snapshotCopyCancelWriter{cancel: cancel}
	source := io.TeeReader(io.NewSectionReader(file, 0, artifact.size), observed)
	subject, err := snapshotHeldBytes(parent, source, int64(len(raw)), artifact.digest, artifact.size)
	if subject != nil || packerErrorCode(err) != e.CodeTimeout || !errors.Is(err, context.Canceled) || observed.bytes <= 0 || observed.bytes >= len(raw) {
		t.Fatalf("mid-copy cancellation: snapshot=%v bytes-read=%d error=%v", subject, observed.bytes, err)
	}
	subject, err = artifact.snapshotHeldContext(t.Context(), int64(len(raw)))
	if err != nil {
		t.Fatalf("cancellation damaged original artifact: %v", err)
	}
	defer subject.Close()
	if subject.Digest() != artifact.Digest() || subject.Size() != artifact.Size() {
		t.Fatal("retry no longer binds original bytes")
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
				held, err := artifact.snapshotHeldContext(ctx, 1024)
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
		{"invalid digest", e.CodeVerifyFailed}, {"metadata I/O failure", e.CodeEngineCapability},
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
			original := artifact
			t.Cleanup(func() { _ = original.Close() })
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
			case "invalid digest":
				artifact.digest = "tag"
			case "metadata I/O failure":
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				// Cleanup must not close the deliberately faulted descriptor twice.
				t.Cleanup(func() { original.file = nil })
			case "changed size":
				artifact.size++
			case "changed bytes":
				artifact.digest = FileDigest([]byte("different bytes"))
			}
			held, err := artifact.snapshotHeldContext(ctx, budget)
			if held != nil || packerErrorCode(err) != test.code {
				t.Fatalf("rejected snapshot = %v, %v", held, err)
			}
		})
	}
}
