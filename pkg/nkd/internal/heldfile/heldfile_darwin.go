// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package heldfile

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func freezeMutable(ctx context.Context, source *os.File, label string, executable bool, size int64) (*os.File, error) {
	if executable {
		return nil, ErrUnsupported
	}
	dir, err := os.MkdirTemp("", "nkd-held-"+label+"-*")
	if err != nil {
		return nil, fmt.Errorf("create held directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("seal held directory: %w", err)
	}
	path := filepath.Join(dir, label)
	writer, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create held %s: %w", label, err)
	}
	fail := func(cause error) (*os.File, error) {
		_ = writer.Close()
		return nil, cause
	}
	writtenSize, _, err := copySparseHash(ctx, writer, io.NewSectionReader(source, 0, size), size)
	if err != nil {
		return fail(fmt.Errorf("write held %s: %w", label, err))
	}
	if writtenSize != size {
		return fail(fmt.Errorf("held %s changed while being frozen", label))
	}
	if err := writer.Sync(); err != nil {
		return fail(fmt.Errorf("sync held %s: %w", label, err))
	}
	mode := os.FileMode(0o400)
	if executable {
		mode = 0o500
	}
	if err := writer.Chmod(mode); err != nil {
		return fail(fmt.Errorf("chmod held %s: %w", label, err))
	}
	written, err := writer.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat held %s: %w", label, err))
	}
	reader, err := os.Open(path)
	if err != nil {
		return fail(fmt.Errorf("reopen held %s read-only: %w", label, err))
	}
	readInfo, err := reader.Stat()
	if err != nil || !os.SameFile(written, readInfo) {
		_ = reader.Close()
		return fail(fmt.Errorf("held %s changed before unlink", label))
	}
	if err := os.Remove(path); err != nil {
		_ = reader.Close()
		return fail(fmt.Errorf("unlink held %s: %w", label, err))
	}
	if err := writer.Close(); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("close writable held %s: %w", label, err)
	}
	if err := source.Close(); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("close mutable held %s: %w", label, err)
	}
	return reader, nil
}

func newMutable(label string) (*os.File, error) {
	dir, err := os.MkdirTemp("", "nkd-held-output-"+label+"-*")
	if err != nil {
		return nil, fmt.Errorf("create held output directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("seal held output directory: %w", err)
	}
	path := filepath.Join(dir, label)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create held output %s: %w", label, err)
	}
	if err := os.Remove(path); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("unlink held output %s: %w", label, err)
	}
	return f, nil
}

func platformChildPath(fd int) (string, error) { return fmt.Sprintf("/dev/fd/%d", fd), nil }

func platformCommand(args []string) (string, []string, error) {
	return "", nil, ErrUnsupported
}

func platformSupportsExecutable() bool { return false }

func platformOpenSource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
