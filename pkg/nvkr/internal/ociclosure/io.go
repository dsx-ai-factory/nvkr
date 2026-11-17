// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ociclosure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

const pipelineCopyBufferSize = 128 << 10

var pipelineCopyBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, pipelineCopyBufferSize)
	return &buffer
}}

func HashOpenRegularContext(ctx context.Context, f *os.File, before os.FileInfo) (string, int64, os.FileInfo, error) {
	if ctx == nil {
		return "", 0, nil, fmt.Errorf("hashing a regular file requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	if f == nil || before == nil || !before.Mode().IsRegular() {
		return "", 0, nil, fmt.Errorf("cannot hash a non-regular file")
	}
	h := sha256.New()
	size, copyErr := CopyContext(ctx, h, f)
	after, statErr := f.Stat()
	if copyErr != nil {
		return "", 0, nil, copyErr
	}
	if statErr != nil {
		return "", 0, nil, statErr
	}
	if !os.SameFile(before, after) || size != before.Size() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		return "", 0, nil, fmt.Errorf("file changed while hashing")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), size, after, nil
}

type pipelineContextReader struct {
	ctx    context.Context
	source io.Reader
}

func CopyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("copy requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	buffer := pipelineCopyBufferPool.Get().(*[]byte)
	defer pipelineCopyBufferPool.Put(buffer)
	return io.CopyBuffer(destination, pipelineContextReader{ctx: ctx, source: source}, *buffer)
}

func (reader pipelineContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
