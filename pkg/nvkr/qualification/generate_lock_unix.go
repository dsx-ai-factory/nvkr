// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package qualification

import (
	"context"
	"errors"
	"os"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func openQualificationFile(fs *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	return fs.OpenFile(name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, mode)
}
func acquireQualificationLock(ctx context.Context, fs *os.Root, name string) (func(), error) {
	if err := generationContext(ctx); err != nil {
		return nil, err
	}
	// Exclusive initial creation avoids concurrent O_CREAT/open races on Darwin.
	// Existing lock files are opened without creation and their inode is compared
	// with the no-follow observation before locking; symlinks never own the lock.
	file, err := openQualificationFile(fs, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		before, statErr := fs.Lstat(name)
		if statErr != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() != 0 {
			return nil, e.New(e.CodeWriteConflict, "qualification generation lock is not an empty owner-only regular file")
		}
		file, err = openQualificationFile(fs, name, os.O_RDWR, 0)
		if err == nil {
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(before, opened) {
				_ = file.Close()
				return nil, e.New(e.CodeWriteConflict, "qualification generation lock changed while opening")
			}
		}
	}
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err, "opening qualification generation lock")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		_ = file.Close()
		return nil, e.New(e.CodeWriteConflict, "qualification generation lock is not an empty owner-only regular file")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, e.Wrap(e.CodeWriteConflict, err, "locking qualification generation")
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for qualification generation lock")
		case <-ticker.C:
		}
	}
}

func qualificationFileSupport() error { return nil }
