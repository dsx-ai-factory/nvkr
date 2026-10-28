// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseasset

import (
	"archive/zip"
	"context"
	"io"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Select uses the standard library archive readers over an already bounded
// carrier. An empty format retains the original tar-gzip IR representation.
// Callers authenticate the carrier and selected executable independently.
func Select(ctx context.Context, source io.ReaderAt, size int64, format, member string, limits Limits) ([]byte, error) {
	if ctx == nil || source == nil {
		return nil, e.New(e.CodeUsage, "release archive selection requires a context and reader")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release archive selection canceled")
	}
	if size <= 0 || size > MaxToolTransportBytes {
		return nil, e.New(e.CodeVerifyFailed, "release archive is empty or exceeds its transport byte limit")
	}
	switch format {
	case "", "tar-gzip":
		return SelectTarGzip(ctx, io.NewSectionReader(source, 0, size), member, limits)
	case "zip":
		return selectZip(ctx, source, size, member, limits)
	default:
		return nil, e.New(e.CodeInvalidCatalog, "unsupported release archive format %q", format)
	}
}

func selectZip(ctx context.Context, source io.ReaderAt, size int64, member string, limits Limits) ([]byte, error) {
	if !validMember(member) || limits.ExecutableBytes <= 0 || limits.ExpandedBytes < limits.ExecutableBytes ||
		limits.ExpandedBytes == int64(^uint64(0)>>1) || limits.Entries <= 0 {
		return nil, e.New(e.CodeInvalidCatalog, "release archive selection requires a canonical member and bounded limits")
	}
	reader, err := zip.NewReader(contextReaderAt{ctx: ctx, source: source}, size)
	if err != nil {
		return nil, archiveError(ctx, err, "opening ZIP release archive")
	}
	if len(reader.File) > limits.Entries {
		return nil, e.New(e.CodeVerifyFailed, "release archive exceeds its entry limit")
	}
	seen := make(map[string]bool)
	var selected []byte
	var expanded int64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "release archive selection canceled")
		}
		name := strings.TrimPrefix(file.Name, "./")
		directory := file.Mode().IsDir()
		if directory {
			name = strings.TrimSuffix(name, "/")
		}
		if !validMember(name) || seen[name] || (!file.Mode().IsRegular() && !directory) ||
			file.Flags&1 != 0 || (directory && file.UncompressedSize64 != 0) {
			return nil, e.New(e.CodeVerifyFailed, "release archive contains unsafe, duplicate or unsupported members")
		}
		seen[name] = true
		if file.UncompressedSize64 > uint64(limits.ExpandedBytes-expanded) {
			return nil, e.New(e.CodeVerifyFailed, "release archive exceeds its expanded byte limit")
		}
		size := int64(file.UncompressedSize64) // bounded above before conversion
		expanded += size
		if name == member && (directory || size == 0 || size > limits.ExecutableBytes) {
			return nil, e.New(e.CodeVerifyFailed, "release executable is not one bounded nonempty regular member")
		}
		content, err := file.Open()
		if err != nil {
			return nil, archiveError(ctx, err, "opening release member")
		}
		// Read every member through EOF, including those not selected, so the
		// standard decoder checks its length and CRC before returning any bytes.
		bounded := io.LimitReader(contextReader{ctx: ctx, source: content}, size+1)
		var count int64
		if name == member {
			selected, err = io.ReadAll(bounded)
			count = int64(len(selected))
		} else {
			count, err = io.Copy(io.Discard, bounded)
		}
		closeErr := content.Close()
		if err != nil {
			return nil, archiveError(ctx, err, "reading release member")
		}
		if closeErr != nil {
			return nil, archiveError(ctx, closeErr, "closing release member")
		}
		if count != size {
			return nil, e.New(e.CodeVerifyFailed, "release member size differs from its declaration")
		}
	}
	if selected == nil {
		return nil, e.New(e.CodeVerifyFailed, "release archive lacks its exact executable member")
	}
	return selected, nil
}

type contextReaderAt struct {
	ctx    context.Context
	source io.ReaderAt
}

func (reader contextReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.ReadAt(buffer, offset)
}
