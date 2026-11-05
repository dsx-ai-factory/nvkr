// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const producerOverlayGuard = ".nvkr-producer-overlays.lease"

// writeOverlayFiles stages the complete set before mutation and CAS-checks all
// destinations under one persistent kernel-released writer guard. Renames are
// atomic per file, not across the set. Deterministic regeneration/check detects
// and repairs interrupted sets before Refresh can publish a candidate.
func writeOverlayFiles(ctx context.Context, files []overlayFile) error {
	return writeOverlayFilesWithRename(ctx, files, os.Rename)
}

// The rename seam allows deterministic interruption after a published member.
func writeOverlayFilesWithRename(ctx context.Context, files []overlayFile, rename func(string, string) error) error {
	if err := ctx.Err(); err != nil {
		return contextError(ctx, err)
	}
	if err := checkOverlayFiles(files); err == nil {
		return nil
	}
	fd, err := syscall.Open(producerOverlayGuard, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "opening producer overlay writer guard")
	}
	guard := os.NewFile(uintptr(fd), producerOverlayGuard)
	defer func() { _ = guard.Close() }()
	info, err := guard.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return e.New(e.CodeWriteConflict, "producer overlay writer guard must be a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return e.New(e.CodeWriteConflict, "another producer overlay writer is active")
		}
		return e.Wrap(e.CodeWriteConflict, err, "locking producer overlay writer guard")
	}
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()
	for _, file := range files {
		current, err := readOverlayInput(ctx, file.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, file.before) {
			return e.New(e.CodeWriteConflict, "producer overlay %q changed since rendering", file.path)
		}
	}
	type stagedFile struct{ path, temporary string }
	var staged []stagedFile
	defer func() {
		for _, file := range staged {
			_ = os.Remove(file.temporary)
		}
	}()
	for _, file := range files {
		if bytes.Equal(file.before, file.candidate) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return contextError(ctx, err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(file.path), ".producer-overlay-stage-*")
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "staging producer overlay %q", file.path)
		}
		staged = append(staged, stagedFile{file.path, tmp.Name()})
		if err := stageOverlay(ctx, tmp, file.candidate); err != nil {
			return contextError(ctx, e.Wrap(e.CodeInternal, err, "staging producer overlay %q", file.path))
		}
	}
	for _, file := range staged {
		if err := ctx.Err(); err != nil {
			return contextError(ctx, err)
		}
		if err := admitPath(file.path); err != nil {
			return err
		}
		if err := rename(file.temporary, file.path); err != nil {
			return e.Wrap(e.CodeInternal, err, "publishing producer overlay %q", file.path)
		}
		if err := syncOverlayDirectory(filepath.Dir(file.path)); err != nil {
			return e.Wrap(e.CodeInternal, err, "syncing producer overlay %q", file.path)
		}
	}
	return nil
}

func stageOverlay(ctx context.Context, file *os.File, raw []byte) error {
	defer func() { _ = file.Close() }()
	if err := ctx.Err(); err != nil {
		return contextError(ctx, err)
	}
	if err := file.Chmod(0o644); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func syncOverlayDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}
