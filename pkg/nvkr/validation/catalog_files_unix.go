// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
		return nil, fmt.Errorf("configuration root must be an existing directory, not a symlink: %w", err)
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

// names lists one held directory beneath the root ("" is the root itself),
// refusing symlinked or non-directory components and more than limit entries.
func (r *catalogRoot) names(ctx context.Context, path string, limit int) ([]string, error) {
	if ctx == nil {
		return nil, usage("directory listing requires context")
	}
	parts := []string{"."}
	if path != "" {
		if !safeEntryPath(path) {
			return nil, usage("unsafe catalog path")
		}
		parts = strings.Split(path, "/")
	}
	current := r.file
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			if current != r.file {
				_ = current.Close()
			}
			return nil, err
		}
		fd, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if current != r.file {
			_ = current.Close()
		}
		if err != nil {
			return nil, fmt.Errorf("%q must be a directory, not a symlink: %w", path, err)
		}
		current = os.NewFile(uintptr(fd), part)
	}
	defer func() { _ = current.Close() }()
	names, err := current.Readdirnames(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > limit {
		return nil, usage("catalog directory exceeds its entry limit")
	}
	sort.Strings(names)
	return names, nil
}
