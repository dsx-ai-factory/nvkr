// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	archiveContentMethod = "archive-content/v1"
	maxArchiveExpanded   = 256 << 20
	maxArchiveMember     = 64 << 20
	maxArchiveRecords    = 65536
)

// inspectArchiveContents records exact regular member bytes, not discovered
// packages or installed paths. The standard library owns tar/gzip decoding;
// nothing is extracted, followed, executed or resolved outside the held archive.
func inspectArchiveContents(ctx context.Context, source io.Reader) ([]Component, error) {
	buffered := bufio.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(source)})
	var expanded io.Reader = buffered
	if magic, _ := buffered.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, archiveReadError(ctx, err)
		}
		defer func() { _ = compressed.Close() }()
		expanded = compressed
	}
	limited := &io.LimitedReader{R: filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(expanded)}, N: maxArchiveExpanded + 1}
	reader := tar.NewReader(limited)
	paths := map[string]byte{}
	var components []Component
	for records := 0; ; records++ {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "inspecting archive contents")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveReadError(ctx, err)
		}
		if records >= maxArchiveRecords || limited.N <= 0 {
			return nil, e.New(e.CodeVerifyFailed, "content archive exceeds its record or expansion bound")
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !archiveMemberPath(name) || paths[name] != 0 {
			return nil, e.New(e.CodeVerifyFailed, "content archive has an unsafe or duplicate member %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return nil, e.New(e.CodeVerifyFailed, "content archive member %q is not a regular file or directory", name)
		}
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return nil, e.New(e.CodeVerifyFailed, "content archive member %q is sparse", name)
			}
		}
		if header.Size < 0 || header.Size > maxArchiveMember || (header.Typeflag == tar.TypeDir && header.Size != 0) {
			return nil, e.New(e.CodeVerifyFailed, "content archive member %q exceeds its byte bound", name)
		}
		paths[name] = header.Typeflag
		if header.Typeflag == tar.TypeDir {
			continue
		}
		hash := sha256.New()
		count, err := io.Copy(hash, reader)
		if err != nil {
			return nil, archiveReadError(ctx, err)
		}
		if count != header.Size || limited.N <= 0 {
			return nil, e.New(e.CodeVerifyFailed, "content archive member is truncated or exceeds expansion bound")
		}
		components = append(components, Component{Type: "file", Name: name, Hashes: []Hash{{Alg: "SHA-256", Content: hex.EncodeToString(hash.Sum(nil))}}})
	}
	// tar.Reader stops at its terminator. Consume the remainder to authenticate
	// gzip checksums and reject appended content, retaining the expansion bound.
	if _, err := io.Copy(archivePaddingWriter{}, limited); err != nil {
		return nil, archiveReadError(ctx, err)
	}
	if limited.N <= 0 {
		return nil, e.New(e.CodeVerifyFailed, "content archive exceeds expansion bound")
	}
	if archivePathConflict(paths) {
		return nil, e.New(e.CodeVerifyFailed, "content archive has conflicting file and directory paths")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "archive content inspection canceled")
	}
	if len(components) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "content archive contains no regular members")
	}
	slices.SortFunc(components, func(a, b Component) int { return strings.Compare(a.Name, b.Name) })
	return components, nil
}

func archiveMemberPath(name string) bool {
	return name != "." && len(name) <= 4096 && fs.ValidPath(name) && path.Clean(name) == name &&
		canonicalText(name) && !strings.ContainsAny(name, "\\\x00\r\n\t")
}

func archivePathConflict(paths map[string]byte) bool {
	for name := range paths {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if paths[parent] == tar.TypeReg {
				return true
			}
		}
	}
	return false
}

func archiveReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "archive content inspection canceled")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "reading complete content archive")
}

type archivePaddingWriter struct{}

func (archivePaddingWriter) Write(raw []byte) (int, error) {
	for _, value := range raw {
		if value != 0 {
			return 0, e.New(e.CodeVerifyFailed, "content archive has non-padding bytes after its tar terminator")
		}
	}
	return len(raw), nil
}
