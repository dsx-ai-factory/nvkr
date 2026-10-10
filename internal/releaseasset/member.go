// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package releaseasset selects one regular executable from a bounded upstream
// release archive. Selection does not authenticate the archive or issue a receipt;
// callers must verify both its transport digest and the selected executable digest.
package releaseasset

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type Limits struct {
	ExecutableBytes int64
	ExpandedBytes   int64
	Entries         int
}

// MaxToolTransportBytes bounds a source tool's compressed or raw release carrier.
const MaxToolTransportBytes = 256 << 20

// ToolLimits returns a fresh copy of the source-tool archive policy shared by
// maintenance pinning and executable acquisition. Callers cannot mutate a global
// policy value and make the two acquisition boundaries diverge.
func ToolLimits() Limits {
	return Limits{ExecutableBytes: MaxToolTransportBytes, ExpandedBytes: 1 << 30, Entries: 4096}
}

// SelectTarGzip uses the standard library's tar and gzip decoders. It scans the
// complete archive, never extracts paths, and rejects duplicate names, links,
// special files, metadata-only global headers and malformed or oversized trailing
// content. The supplied reader
// must itself be bounded and cancellation-aware when it can block on external I/O.
func SelectTarGzip(ctx context.Context, source io.Reader, member string, limits Limits) ([]byte, error) {
	if ctx == nil || source == nil {
		return nil, e.New(e.CodeUsage, "release archive selection requires a context and reader")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release archive selection canceled")
	}
	if !validMember(member) || limits.ExecutableBytes <= 0 || limits.ExpandedBytes < limits.ExecutableBytes ||
		limits.ExpandedBytes == int64(^uint64(0)>>1) || limits.Entries <= 0 {
		return nil, e.New(e.CodeInvalidCatalog, "release archive selection requires a canonical member and bounded limits")
	}
	gz, err := gzip.NewReader(contextReader{ctx: ctx, source: source})
	if err != nil {
		return nil, archiveError(ctx, err, "opening release archive")
	}
	defer func() { _ = gz.Close() }()
	expanded := &io.LimitedReader{R: gz, N: limits.ExpandedBytes + 1}
	reader := tar.NewReader(expanded)
	seen := make(map[string]bool)
	var selected []byte
	var memberBytes int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "release archive selection canceled")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(ctx, err, "reading release archive header")
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if count >= limits.Entries || !validMember(name) || seen[name] || header.Size < 0 ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) ||
			(header.Typeflag == tar.TypeDir && header.Size != 0) {
			return nil, e.New(e.CodeVerifyFailed, "release archive contains unsafe, duplicate or unsupported members")
		}
		seen[name] = true
		// archive/tar can synthesize sparse holes without reading gzip bytes.
		// Bound logical member sizes as well as the physical expanded stream.
		if header.Size > limits.ExpandedBytes-memberBytes {
			return nil, e.New(e.CodeVerifyFailed, "release archive exceeds its logical member byte limit")
		}
		memberBytes += header.Size
		if name == member {
			if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > limits.ExecutableBytes {
				return nil, e.New(e.CodeVerifyFailed, "release executable is not one bounded nonempty regular member")
			}
			selected, err = io.ReadAll(contextReader{ctx: ctx, source: reader})
			if err != nil || int64(len(selected)) != header.Size {
				return nil, archiveError(ctx, err, "reading release executable")
			}
		} else if _, err := io.Copy(io.Discard, contextReader{ctx: ctx, source: reader}); err != nil {
			return nil, archiveError(ctx, err, "reading remaining release member")
		}
	}
	// tar EOF can precede gzip EOF. Consume padding to authenticate the gzip
	// checksum and reject a concatenated hidden archive rather than ignoring it.
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "release archive trailer scan canceled")
		}
		n, err := expanded.Read(buffer)
		for _, value := range buffer[:n] {
			if value != 0 {
				return nil, e.New(e.CodeVerifyFailed, "release archive has non-padding trailing content")
			}
		}
		if expanded.N <= 0 {
			return nil, e.New(e.CodeVerifyFailed, "release archive exceeds its expanded byte limit")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(ctx, err, "reading release archive trailer")
		}
	}
	if selected == nil {
		return nil, e.New(e.CodeVerifyFailed, "release archive lacks its exact executable member")
	}
	return selected, nil
}

func validMember(name string) bool {
	return len(name) <= 4096 && name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, "\\\x00")
}

func archiveError(ctx context.Context, err error, operation string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s canceled", operation)
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
