// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package heldfile

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func freezeMutable(_ context.Context, f *os.File, label string, executable bool, _ int64) (*os.File, error) {
	mode := uint32(0o400)
	if executable {
		mode = 0o500
	}
	if err := unix.Fchmod(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chmod held %s: %w", label, err)
	}
	const seals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seal held %s: %w", label, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("rewind held %s: %w", label, err)
	}
	return f, nil
}

func newMutable(label string) (*os.File, error) { return linuxMemfd(label) }

func linuxMemfd(label string) (*os.File, error) {
	fd, err := unix.MemfdCreate("nkd-"+label, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create sealed memfd for %s: %w", label, err)
	}
	return os.NewFile(uintptr(fd), "memfd:nkd-"+label), nil
}

func platformChildPath(fd int) (string, error) {
	return fmt.Sprintf("/proc/self/fd/%d", fd), nil
}

func platformCommand(args []string) (string, []string, error) {
	path, _ := platformChildPath(3)
	return path, args, nil
}

func platformSupportsExecutable() bool { return true }

func platformOpenSource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
