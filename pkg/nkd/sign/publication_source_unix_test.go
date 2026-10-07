// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package sign

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPublicationRecoveryReadsRejectSymlinkAndFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte(`{"schemaVersion":"nkd.signed-publication-pending/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{symlink, fifo} {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			type result struct {
				journal error
				digest  error
			}
			done := make(chan result, 1)
			go func() {
				_, journalErr := readPublicationJournal(context.Background(), path)
				_, digestErr := digestRegularFile(context.Background(), path)
				done <- result{journal: journalErr, digest: digestErr}
			}()
			select {
			case got := <-done:
				if got.journal == nil || got.digest == nil {
					t.Fatalf("publication recovery accepted unsafe %s: journal=%v digest=%v",
						path, got.journal, got.digest)
				}
			case <-time.After(time.Second):
				t.Fatalf("publication recovery blocked while opening %s", path)
			}
		})
	}
}
