// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package cistore

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// OpenGitHubOutput holds an existing regular provider output before transfer
// work. It follows the native factory summary pattern without creating a file,
// following a final symlink or blocking on a substituted FIFO.
func OpenGitHubOutput(ctx context.Context, path string) (*os.File, error) {
	if ctx == nil || path == "" {
		return nil, e.New(e.CodeUsage, "private CI transfer requires context and GITHUB_OUTPUT")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "private CI provider output admission canceled")
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "holding private CI provider output")
	}
	file := os.NewFile(uintptr(fd), "private CI provider output")
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, e.New(e.CodeImmutableViolation, "private CI provider output must be a regular file")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, e.Wrap(e.CodeTimeout, err, "private CI provider output admission canceled")
	}
	return file, nil
}
