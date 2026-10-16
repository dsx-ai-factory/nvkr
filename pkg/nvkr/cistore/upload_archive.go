// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// An upload owns either an existing single-file carrier or the generic packing
// fallback. Single-file TAR bytes are virtual: a bounded header, the held source
// inode, and zero padding/footer. No payload-sized staging file is created.
type uploadArchive struct {
	reader    io.ReaderAt
	file      *os.File
	info      os.FileInfo
	size      int64
	temporary string
	virtual   bool
}

func openUploadArchive(ctx context.Context, root string, paths []string) (*uploadArchive, error) {
	if len(paths) == 1 {
		file, info, err := holdSingleUpload(ctx, root, paths[0])
		if err != nil {
			return nil, err
		}
		if file != nil {
			reader, err := newVirtualUpload(ctx, file, info, paths[0])
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			return &uploadArchive{reader: reader, file: file, info: info, size: reader.size, virtual: true}, nil
		}
	}
	temporary, err := os.MkdirTemp(root, ".nvkr-ci-upload-*")
	if err != nil {
		return nil, safeIOError(ctx, e.CodeBuildFailed, "creating private CI carrier staging")
	}
	archive := filepath.Join(temporary, "payload.tar")
	if err := transportarchive.Pack(ctx, archive, root, paths); err != nil {
		_ = os.RemoveAll(temporary)
		return nil, err
	}
	file, info, err := securepath.OpenRegular(archive)
	if err != nil {
		_ = os.RemoveAll(temporary)
		return nil, safeIOError(ctx, e.CodeVerifyFailed, "holding packed CI carrier")
	}
	return &uploadArchive{reader: file, file: file, info: info, size: info.Size(), temporary: temporary}, nil
}

func (archive *uploadArchive) close() {
	_ = archive.file.Close()
	if archive.temporary != "" {
		_ = os.RemoveAll(archive.temporary)
	}
}

func (archive *uploadArchive) unchanged(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return safeIOError(ctx, e.CodeVerifyFailed, "checking held CI upload")
	}
	current, err := archive.file.Stat()
	if err != nil || !securepath.SameMetadata(archive.info, current) {
		return e.New(e.CodeVerifyFailed, "held CI upload changed during transfer")
	}
	return nil
}

func (archive *uploadArchive) verify(ctx context.Context, digest string) error {
	if err := archive.unchanged(ctx); err != nil {
		return err
	}
	if archive.virtual {
		// Registry deduplication may skip reading existing chunks. Rehash the
		// held source regardless, including same-metadata byte changes, before
		// returning any locator. Chunk digests still bind every transmitted byte.
		_, actual, err := chunkArchive(ctx, archive.reader, archive.size, maxChunkBytes)
		if err != nil {
			return err
		}
		if actual != digest {
			return e.New(e.CodeVerifyFailed, "held CI upload content changed during transfer")
		}
	}
	return archive.unchanged(ctx)
}

// The final member may be a directory, which selects the existing generic
// packer. Unsafe parents, links, devices and substituted nonregular files fail;
// they never select a permissive fallback. The returned file belongs to caller.
func holdSingleUpload(ctx context.Context, root, relative string) (*os.File, os.FileInfo, error) {
	if !safePath(relative) {
		return nil, nil, e.New(e.CodeVerifyFailed, "CI upload has an unsafe workspace member")
	}
	directory, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, nil, safeIOError(ctx, e.CodeVerifyFailed, "holding CI upload workspace")
	}
	defer func() { _ = directory.Close() }()
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return nil, nil, safeIOError(ctx, e.CodeVerifyFailed, "holding CI upload parents")
		}
		next, _, err := securepath.OpenChild(directory, part, true)
		if err != nil {
			return nil, nil, safeIOError(ctx, e.CodeVerifyFailed, "CI upload requires no-follow workspace parents")
		}
		_ = directory.Close()
		directory = next
	}
	entry, err := securepath.InspectChild(directory, parts[len(parts)-1], relative)
	if err != nil {
		return nil, nil, safeIOError(ctx, e.CodeVerifyFailed, "holding CI upload member")
	}
	if entry.Kind == "file" {
		return entry.File, entry.Info, nil
	}
	if entry.File != nil {
		_ = entry.File.Close()
	}
	if entry.Kind == "directory" {
		return nil, nil, nil
	}
	return nil, nil, e.New(e.CodeVerifyFailed, "CI upload requires a regular file or directory")
}

const maxVirtualUploadHeaderBytes = 64 << 10

type virtualUpload struct {
	ctx    context.Context
	file   *os.File
	header []byte
	length int64
	size   int64
}

func newVirtualUpload(ctx context.Context, file *os.File, info os.FileInfo, relative string) (*virtualUpload, error) {
	if ctx == nil || file == nil || info == nil || !info.Mode().IsRegular() || !safePath(relative) ||
		info.Size() < 0 || info.Size() > transportarchive.MaxArchiveBytes {
		return nil, e.New(e.CodeVerifyFailed, "virtual CI upload requires a bounded held regular file")
	}
	if ctx.Err() != nil {
		return nil, safeIOError(ctx, e.CodeVerifyFailed, "preparing virtual CI upload")
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return nil, e.New(e.CodeVerifyFailed, "encoding virtual CI upload header")
	}
	// Keep the single-member representation byte-identical to
	// transportarchive.Pack. Compatibility cases compare the complete streams.
	header.Name = relative
	header.ModTime, header.AccessTime, header.ChangeTime = time.Unix(0, 0), time.Time{}, time.Time{}
	header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
	header.Mode &= 0o777
	header.Format = tar.FormatPAX
	var encoded virtualHeaderBuffer
	writer := tar.NewWriter(&encoded)
	if err := writer.WriteHeader(header); err != nil {
		return nil, e.New(e.CodeVerifyFailed, "virtual CI upload header exceeds its format bound")
	}
	// WriteHeader emits the complete PAX/regular headers. Closing this writer
	// would require copying the payload; ReaderAt supplies it and the standard
	// 512-byte record padding plus two terminal zero records instead.
	size := int64(encoded.Len()) + info.Size() + (512-info.Size()%512)%512 + 1024
	if size > transportarchive.MaxArchiveBytes {
		return nil, e.New(e.CodeVerifyFailed, "virtual CI upload exceeds the archive byte bound")
	}
	return &virtualUpload{ctx: ctx, file: file, header: encoded.Bytes(), length: info.Size(), size: size}, nil
}

type virtualHeaderBuffer struct{ buffer bytes.Buffer }

func (buffer *virtualHeaderBuffer) Len() int      { return buffer.buffer.Len() }
func (buffer *virtualHeaderBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func (buffer *virtualHeaderBuffer) Write(raw []byte) (int, error) {
	if len(raw) > maxVirtualUploadHeaderBytes-buffer.Len() {
		return 0, e.New(e.CodeVerifyFailed, "virtual CI upload header exceeds its memory bound")
	}
	return buffer.buffer.Write(raw)
}

// ReaderAt may be used concurrently by registry retries and independent chunk
// uploads. Immutable framing and File.ReadAt do not share a seek cursor; each
// source read is bounded so cancellation is observed between regular-file I/O.
func (archive *virtualUpload) ReadAt(raw []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, e.New(e.CodeVerifyFailed, "virtual CI upload read has a negative offset")
	}
	read := 0
	for len(raw) > 0 {
		if archive.ctx.Err() != nil {
			return read, safeIOError(archive.ctx, e.CodeVerifyFailed, "reading virtual CI upload")
		}
		if offset >= archive.size {
			return read, io.EOF
		}
		headerEnd := int64(len(archive.header))
		length := min(int64(len(raw)), archive.size-offset, int64(128<<10))
		switch {
		case offset < headerEnd:
			length = min(length, headerEnd-offset)
			copy(raw[:length], archive.header[offset:offset+length])
		case offset < headerEnd+archive.length:
			length = min(length, headerEnd+archive.length-offset)
			n, err := archive.file.ReadAt(raw[:length], offset-headerEnd)
			if err != nil || int64(n) != length {
				return read + n, safeIOError(archive.ctx, e.CodeVerifyFailed, "reading held CI upload member")
			}
		default:
			clear(raw[:length])
		}
		read += int(length)
		offset += length
		raw = raw[length:]
	}
	return read, nil
}
