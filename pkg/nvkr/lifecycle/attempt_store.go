// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func recordRunFailure(ctx context.Context, path string, run *Run, cause error, operation string) (*Run, error) {
	failed := failedRun(run, cause)
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	commitCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	if err := writeRunRecord(commitCtx, path, failed); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(cause, err), "%s", operation)
	}
	return failed, cause
}

func writeRunRecord(ctx context.Context, path string, run *Run) error {
	_, err := writeRunRecordState(ctx, path, run)
	return err
}

// writeRunRecordState reports whether the immutable record became visible even
// when the following directory sync failed. Local disk execution uses that bit
// to roll back the record and payload as one attempt transaction.
func writeRunRecordState(ctx context.Context, path string, run *Run) (linked bool, returnErr error) {
	return writeRunRecordStateWithSync(ctx, path, run, func(directory *os.File) error {
		return directory.Sync()
	})
}

func writeRunRecordStateWithSync(ctx context.Context, path string, run *Run,
	syncDirectory func(*os.File) error,
) (linked bool, returnErr error) {
	if syncDirectory == nil {
		return false, e.New(e.CodeInternal, "run record directory sync is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return false, e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	raw, err := canonical.Marshal(run)
	if err != nil {
		return false, err
	}
	return writeLifecycleRecordState(ctx, path, raw, syncDirectory)
}

// writeLifecycleRecordState commits generated descriptive records without
// replacing an earlier record, using the same durability as the Run owner.
func writeLifecycleRecordState(ctx context.Context, path string, raw []byte, syncDirectory func(*os.File) error) (linked bool, returnErr error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".lifecycle-*.json")
	if err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "staging immutable run record")
	}
	stage := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(stage) }()
	if _, err := file.Write(raw); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "writing run record")
	}
	if err := file.Sync(); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "syncing run record")
	}
	if err := file.Chmod(0o444); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "protecting run record")
	}
	if err := file.Sync(); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "syncing protected run record")
	}
	if err := file.Close(); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "closing run record")
	}
	if err := ctx.Err(); err != nil {
		return false, e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	if err := os.Link(stage, path); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "committing immutable run record")
	}
	linked = true
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, e.Wrap(e.CodeImmutableViolation, err, "opening run directory")
	}
	defer func() { _ = directory.Close() }()
	if err := syncDirectory(directory); err != nil {
		return true, e.Wrap(e.CodeImmutableViolation, err, "syncing run directory")
	}
	return true, nil
}
