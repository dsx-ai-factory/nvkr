// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"sync"
)

// Repeated closure, source and presentation checks consume the same verified
// bytes within one inspection. This bounded, private memo is never persisted or
// reused across calls. Its entries are memory snapshots, not path-based authority.
// Cache hits still read and hash every distinct blob anew on each visit.
type metadataReads struct {
	mu    sync.Mutex
	bytes int64
	blobs map[string][]byte
}
type metadataReadsKey struct{}

const maxMetadataReadMemo = 32 << 20

func withMetadataReads(ctx context.Context) context.Context {
	if ctx == nil || ctx.Value(metadataReadsKey{}) != nil {
		return ctx
	}
	return freshMetadataReads(ctx)
}

func freshMetadataReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, metadataReadsKey{}, &metadataReads{blobs: make(map[string][]byte)})
}

func (reads *metadataReads) get(path string) ([]byte, bool) {
	if reads == nil {
		return nil, false
	}
	reads.mu.Lock()
	defer reads.mu.Unlock()
	raw, ok := reads.blobs[path]
	return bytes.Clone(raw), ok
}

func (reads *metadataReads) put(path string, raw []byte) {
	if reads == nil {
		return
	}
	reads.mu.Lock()
	defer reads.mu.Unlock()
	if _, exists := reads.blobs[path]; exists || int64(len(raw)) > maxMetadataReadMemo-reads.bytes {
		return
	}
	reads.blobs[path] = bytes.Clone(raw)
	reads.bytes += int64(len(raw))
}
