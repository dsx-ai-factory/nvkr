// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"context"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Hold each directory while traversing; no symlink or FIFO can redirect or block a read.
type catalogRoot struct{ file *os.File }

func openCatalogRoot(ctx context.Context, path string) (*catalogRoot, error) {
	if ctx == nil {
		return nil, usage("catalog root requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return &catalogRoot{os.NewFile(uintptr(fd), path)}, nil
}
func (r *catalogRoot) Close() error { return r.file.Close() }
func (r *catalogRoot) read(ctx context.Context, path string, limit int) ([]byte, error) {
	if !safeEntryPath(path) {
		return nil, usage("unsafe catalog path")
	}
	if ctx == nil {
		return nil, usage("document read requires context")
	}
	current := r.file
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			if current != r.file {
				_ = current.Close()
			}
			return nil, err
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), part, flags, 0)
		if current != r.file {
			_ = current.Close()
		}
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(fd), part)
	}
	defer func() { _ = current.Close() }()
	return readRegular(ctx, current, limit)
}
