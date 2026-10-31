// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package bundlearchive validates deployment bundle archives without extracting or rewriting them.
package bundlearchive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	MaxArchiveBytes  = 512 << 20
	MaxExpandedBytes = 512 << 20
	MaxEntries       = 100_000
	maxPathBytes     = 4096
)

// Entry describes exact file bytes or an explicit directory. Digest is an exact
// file SHA-256, not a structured identity or a dependency-completeness claim.
type Entry struct {
	Path      string
	Mode      int64
	Digest    string
	Size      int64
	Directory bool
}

// Inspect validates one gzip member containing a nonempty tar file inventory.
// Only canonical relative paths, regular files and directories are admitted.
// Results are sorted; compressed bytes, expanded bytes, members and paths are bounded.
func Inspect(ctx context.Context, source io.Reader) ([]Entry, error) {
	return inspect(ctx, source, MaxArchiveBytes, MaxExpandedBytes, MaxEntries)
}

func inspect(ctx context.Context, source io.Reader, compressedLimit, expandedLimit int64, entryLimit int) ([]Entry, error) {
	if ctx == nil || source == nil {
		return nil, e.New(e.CodeUsage, "bundle archive inspection requires a context and reader")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "bundle archive inspection canceled")
	}
	compressed := &io.LimitedReader{R: contextReader{ctx: ctx, source: source}, N: compressedLimit + 1}
	buffered := bufio.NewReader(compressed)
	zipped, err := gzip.NewReader(buffered)
	if err != nil {
		return nil, archiveError(ctx, err, "opening gzip bundle")
	}
	defer func() { _ = zipped.Close() }()
	zipped.Multistream(false)
	expanded := &io.LimitedReader{R: contextReader{ctx: ctx, source: zipped}, N: expandedLimit + 1}
	reader := tar.NewReader(expanded)
	entries := make([]Entry, 0)
	seen := map[string]bool{}
	parents := map[string]bool{}
	files := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(ctx, err, "reading bundle tar")
		}
		name := header.Name
		directory := header.Typeflag == tar.TypeDir
		if directory {
			name = strings.TrimSuffix(name, "/")
		}
		if len(entries) >= entryLimit || !ValidPath(name) ||
			(header.Typeflag != tar.TypeReg && !directory) || header.Linkname != "" ||
			header.Mode < 0 || header.Mode > 0o777 || header.Size < 0 || header.Size > expandedLimit ||
			(directory && header.Size != 0) {
			return nil, e.New(e.CodeVerifyFailed, "bundle contains an unsafe, unsupported or oversized member %q", header.Name)
		}
		for key := range header.PAXRecords {
			switch key {
			case "path", "size", "mtime", "atime", "ctime", "uid", "gid", "uname", "gname", "comment", "charset":
			default:
				return nil, e.New(e.CodeVerifyFailed, "bundle contains unsupported extended metadata %q on %q", key, name)
			}
		}
		if _, exists := seen[name]; exists || !directory && parents[name] {
			return nil, e.New(e.CodeVerifyFailed, "bundle contains duplicate or conflicting path %q", name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if isDir, exists := seen[parent]; exists && !isDir {
				return nil, e.New(e.CodeVerifyFailed, "bundle has a regular-file parent %q", parent)
			}
			parents[parent] = true
			if len(parents) > entryLimit {
				return nil, e.New(e.CodeVerifyFailed, "bundle exceeds its directory bound")
			}
		}
		seen[name] = directory
		entry := Entry{Path: name, Mode: header.Mode, Size: header.Size, Directory: directory}
		if !directory {
			digest := sha256.New()
			if _, err := io.CopyN(digest, reader, header.Size); err != nil {
				return nil, archiveError(ctx, err, "hashing bundle file")
			}
			entry.Digest = "sha256:" + hex.EncodeToString(digest.Sum(nil))
			files++
		}
		entries = append(entries, entry)
	}
	// tar stops at its terminator. Consume all remaining gzip bytes to validate
	// the checksum, and allow only zero tar padding after that terminator.
	var padding, zeros [32 << 10]byte
	for {
		n, err := expanded.Read(padding[:])
		if !bytes.Equal(padding[:n], zeros[:n]) {
			return nil, e.New(e.CodeVerifyFailed, "bundle tar contains trailing data")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(ctx, err, "checking bundle gzip checksum")
		}
	}
	if expanded.N == 0 || compressed.N == 0 {
		return nil, e.New(e.CodeVerifyFailed, "bundle exceeds its byte bounds")
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		if err != nil {
			return nil, archiveError(ctx, err, "checking bundle compressed boundary")
		}
		return nil, e.New(e.CodeVerifyFailed, "bundle contains trailing compressed data")
	}
	if files == 0 {
		return nil, e.New(e.CodeVerifyFailed, "bundle must contain at least one regular file")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "bundle archive inspection canceled")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// ValidPath admits one canonical platform-independent relative archive path.
func ValidPath(name string) bool {
	// Outer whitespace is refused, matching the SBOM component-name domain.
	return name != "" && name != "." && len(name) <= maxPathBytes && utf8.ValidString(name) && strings.TrimSpace(name) == name &&
		!strings.ContainsAny(name, "\\:") && !strings.HasPrefix(name, "/") && path.Clean(name) == name &&
		name != ".." && !strings.HasPrefix(name, "../") && strings.IndexFunc(name, unicode.IsControl) < 0
}

func archiveError(ctx context.Context, err error, operation string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "bundle archive inspection canceled")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
