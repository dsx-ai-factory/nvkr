// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// chunkReader opens one authenticated descriptor at a time. Bytes are still
// untrusted until EOF: the downstream extractor must keep them in private staging
// and authenticate the complete archive before publishing. Each response is
// checked and closed before the next is opened; no pipe or goroutine is needed.
type chunkReader struct {
	ctx         context.Context
	layers      []v1.Layer
	descriptors []v1.Descriptor
	index       int
	source      io.ReadCloser
	hash        hash.Hash
	remaining   int64
	err         error
	closed      bool
}

// The caller admits the manifest and exact layer count before construction.
// Registry layers carry that caller's bounded context into blocking HTTP reads.
func newChunkReader(ctx context.Context, layers []v1.Layer, descriptors []v1.Descriptor) *chunkReader {
	return &chunkReader{ctx: ctx, layers: layers, descriptors: descriptors}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.closed {
		return 0, e.New(e.CodeFetchFailed, "private CI chunk stream is closed")
	}
	if len(p) == 0 {
		return 0, nil
	}
	for emptyReads := 0; ; {
		if r.ctx.Err() != nil {
			return 0, r.fail(safeIOError(r.ctx, e.CodeTimeout, "reading private CI chunk canceled"))
		}
		if r.source == nil {
			if r.index == len(r.layers) {
				return 0, io.EOF
			}
			if err := r.open(); err != nil {
				return 0, r.fail(err)
			}
			if r.ctx.Err() != nil {
				return 0, r.fail(safeIOError(r.ctx, e.CodeTimeout, "opening private CI chunk canceled"))
			}
		}
		// Read one byte beyond the descriptor only as a bounded EOF probe. That
		// byte must never reach the TAR decoder or the complete-archive hash.
		var probe [1]byte
		buffer := p[:min(int64(len(p)), r.remaining)]
		probing := r.remaining == 0
		if probing {
			buffer = probe[:]
		}
		n, readErr := r.source.Read(buffer)
		if probing && n != 0 {
			return 0, r.fail(e.New(e.CodeVerifyFailed, "private CI chunk exceeds its exact size"))
		}
		if !probing && n != 0 {
			_, _ = r.hash.Write(p[:n])
			r.remaining -= int64(n)
		}
		if readErr != nil && readErr != io.EOF {
			return n, r.fail(safeIOError(r.ctx, e.CodeFetchFailed, "reading private CI chunk"))
		}
		if readErr == io.EOF {
			if r.remaining != 0 || hex.EncodeToString(r.hash.Sum(nil)) != r.descriptors[r.index].Digest.Hex {
				return n, r.fail(e.New(e.CodeVerifyFailed, "private CI chunk size or digest mismatch"))
			}
			closeErr := r.source.Close()
			r.source = nil
			if closeErr != nil {
				return n, r.fail(safeIOError(r.ctx, e.CodeFetchFailed, "closing private CI chunk"))
			}
			r.index++
			if n == 0 {
				emptyReads = 0
				continue
			}
		}
		if n != 0 {
			return n, nil
		}
		emptyReads++
		if emptyReads >= 100 {
			return 0, r.fail(safeIOError(r.ctx, e.CodeFetchFailed, "private CI chunk made no read progress"))
		}
	}
}

func (r *chunkReader) open() error {
	layer := r.layers[r.index]
	descriptor := r.descriptors[r.index]
	digest, digestErr := layer.Digest()
	size, sizeErr := layer.Size()
	mediaType, mediaErr := layer.MediaType()
	if digestErr != nil || sizeErr != nil || mediaErr != nil || digest != descriptor.Digest || size != descriptor.Size || mediaType != chunkMediaType {
		return safeIOError(r.ctx, e.CodeVerifyFailed, "CI carrier layer differs from its ordered descriptor")
	}
	source, err := layer.Compressed()
	if err != nil {
		if source != nil {
			_ = source.Close()
		}
		return safeIOError(r.ctx, e.CodeFetchFailed, "opening private CI chunk")
	}
	if source == nil {
		return e.New(e.CodeFetchFailed, "private CI chunk returned no reader")
	}
	r.source, r.hash, r.remaining = source, sha256.New(), descriptor.Size
	return nil
}

func (r *chunkReader) fail(err error) error {
	r.err = err
	if r.source != nil {
		_ = r.source.Close()
		r.source = nil
	}
	return err
}

// Close releases the current response on early TAR rejection or cancellation.
// A fully consumed stream has already closed and checked every response at EOF.
func (r *chunkReader) Close() error {
	r.closed = true
	if r.source != nil {
		err := r.source.Close()
		r.source = nil
		if err != nil {
			return r.fail(safeIOError(r.ctx, e.CodeFetchFailed, "closing private CI chunk"))
		}
	}
	return nil
}
