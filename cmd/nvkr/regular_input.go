// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// These diagnostic inputs remain bounded and tied to the regular file opened;
// decoding them never grants authenticated authority.
func inputContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

const maxRegularInputBytes int64 = 64 << 20

func readBoundedRegularInput(ctx context.Context, path string, maximum int64) ([]byte, error) {
	if err := inputContextError(ctx, "reading input file"); err != nil {
		return nil, err
	}
	if maximum < 0 || maximum > maxRegularInputBytes {
		return nil, e.New(e.CodePipelineDrift,
			"input file %s exceeds the supported size limit", path)
	}
	file, held, err := openRegularInput(path, e.CodePipelineDrift)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if held.Size() > maximum {
		return nil, e.New(e.CodePipelineDrift,
			"input file %s is not a bounded regular file", path)
	}
	raw := make([]byte, 0, held.Size())
	buffer := make([]byte, 128<<10)
	for {
		if err := inputContextError(ctx, "reading input file"); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if int64(len(raw)+n) > maximum {
				return nil, e.New(e.CodePipelineDrift,
					"input file %s grew beyond its expected size", path)
			}
			raw = append(raw, buffer[:n]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	afterHeld, heldErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if heldErr != nil || pathErr != nil || !os.SameFile(held, afterHeld) ||
		!os.SameFile(held, afterPath) || afterHeld.Size() != int64(len(raw)) ||
		afterPath.Size() != int64(len(raw)) || afterHeld.Mode() != held.Mode() ||
		!afterHeld.ModTime().Equal(held.ModTime()) {
		return nil, e.New(e.CodePipelineDrift,
			"input file %s changed while reading", path)
	}
	return raw, nil
}

func openRegularInput(path string, code e.Code) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, e.Wrap(code, err,
			"opening input file %s without following links", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, e.New(code,
			"holding input file %s", path)
	}
	held, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !held.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || !os.SameFile(held, pathInfo) {
		_ = file.Close()
		return nil, nil, e.New(code,
			"input file %s is not a stable regular file", path)
	}
	return file, held, nil
}
