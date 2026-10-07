// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"golang.org/x/sys/unix"
)

func readCanonicalJSONContext(ctx context.Context, path string, destination any) error {
	if err := inputContextError(ctx, "reading canonical status JSON"); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return e.New(e.CodeInternal, "holding canonical status JSON %s", path)
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "stating %s", path)
	}
	pathInfo, pathErr := os.Lstat(path)
	if pathErr != nil || !before.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(before, pathInfo) || before.Size() < 0 || before.Size() > trust.MaxAuthenticatedDocumentBytes {
		return e.New(e.CodeInvalidCatalog, "%s must be a regular JSON document no larger than %d bytes", path, trust.MaxAuthenticatedDocumentBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(canonicalInputReader{ctx: ctx, source: file}, trust.MaxAuthenticatedDocumentBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "reading canonical status JSON %s", path)
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", path)
	}
	if int64(len(raw)) > trust.MaxAuthenticatedDocumentBytes {
		return e.New(e.CodeInvalidCatalog, "%s exceeds the %d-byte JSON document limit", path, trust.MaxAuthenticatedDocumentBytes)
	}
	after, statErr := file.Stat()
	afterPath, afterPathErr := os.Lstat(path)
	if statErr != nil || afterPathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, afterPath) ||
		before.Size() != int64(len(raw)) || after.Size() != before.Size() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) {
		return e.New(e.CodeInvalidCatalog, "canonical status JSON %s changed while reading", path)
	}
	if err := inputContextError(ctx, "reading canonical status JSON"); err != nil {
		return err
	}
	if err := canonical.UnmarshalExact(raw, destination); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "reading canonical JSON %s", path)
	}
	return nil
}

type canonicalInputReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader canonicalInputReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
