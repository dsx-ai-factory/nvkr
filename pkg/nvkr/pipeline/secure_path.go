// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"golang.org/x/sys/unix"
)

// openRegularBeneath resolves every untrusted path component relative to an
// already-open directory descriptor. O_NOFOLLOW on each hop prevents a producer
// from replacing an output directory or the final artifact with a host symlink.
func openRegularBeneath(root, relative string) (*os.File, os.FileInfo, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, nil, fmt.Errorf("unsafe artifact path %q", relative)
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, nil, fmt.Errorf("unsafe artifact path %q", relative)
		}
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening trusted artifact root %s: %w", root, err)
	}
	currentFD := rootFD
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i != len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		nextFD, openErr := unix.Openat(currentFD, part, flags, 0)
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			_ = unix.Close(rootFD)
			return nil, nil, fmt.Errorf("opening artifact %s beneath %s: %w", relative, root, openErr)
		}
		currentFD = nextFD
	}
	_ = unix.Close(rootFD)
	f := os.NewFile(uintptr(currentFD), filepath.Join(root, relative))
	if f == nil {
		_ = unix.Close(currentFD)
		return nil, nil, fmt.Errorf("opening artifact %s beneath %s", relative, root)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("artifact %s beneath %s is not a regular file", relative, root)
	}
	return f, info, nil
}

func hashOpenRegularContext(ctx context.Context, f *os.File, before os.FileInfo) (string, int64, os.FileInfo, error) {
	return ociclosure.HashOpenRegularContext(ctx, f, before)
}

func hashRegularBeneathContext(ctx context.Context, root, relative string) (string, int64, os.FileInfo, error) {
	if ctx == nil {
		return "", 0, nil, fmt.Errorf("hashing an artifact requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	f, before, err := openRegularBeneath(root, relative)
	if err != nil {
		return "", 0, nil, err
	}
	digest, size, after, hashErr := hashOpenRegularContext(ctx, f, before)
	closeErr := f.Close()
	if hashErr != nil {
		return "", 0, nil, hashErr
	}
	if closeErr != nil {
		return "", 0, nil, closeErr
	}
	return digest, size, after, nil
}

type pipelineContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader pipelineContextReader) Read(raw []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(raw)
}
func copyPipelineContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	return ociclosure.CopyContext(ctx, destination, source)
}

func relativeBeneath(root, target string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(absRoot, absTarget)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q is not beneath trusted root %q", target, root)
	}
	if filepath.Clean(relative) != relative {
		return "", fmt.Errorf("artifact path %q is not canonical beneath trusted root", target)
	}
	return relative, nil
}
