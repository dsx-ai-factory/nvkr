// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestWriteAtomicRejectsCanceledContextBeforeOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "output.json")
	err := writeAtomic(ctx, path, []byte("payload"), 0o640)
	if cliErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("got %v, want %s", err, nvkrerr.CodeTimeout)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled write created output: %v", statErr)
	}
}

func TestWriteAtomicPublishesCompleteFileWithRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.json")
	want := []byte("complete payload")
	if err := writeAtomic(context.Background(), path, want, 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o640 {
		t.Fatalf("got mode %o, want 640", gotMode)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".nvkr-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("successful write leaked temporary files: %v", matches)
	}
}
