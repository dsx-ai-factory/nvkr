// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"golang.org/x/sync/semaphore"
)

// Allow two maximum-size bundles plus the overflow-detection bytes.
const maxMetadataReadBytes = 2*sbom.MaxBundleBytes + metadataConcurrency

// Serialize integrity accounting and writes: repeated configs may share a blob
// path even when their owners are fetched concurrently. Limits count acquisition
// bytes, including repeats, just as the sequential pull did.
type metadataBlobWriter struct {
	inflight     *semaphore.Weighted
	mu           sync.Mutex
	writer       layoutWriter
	total, limit int64
}

func (w *metadataBlobWriter) put(raw []byte, d Descriptor, max int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	size := int64(len(raw))
	if size > max || size > w.limit-w.total || d.Size != size || digestBytes(raw) != d.Digest {
		return e.New(e.CodeVerifyFailed, "metadata object exceeds bounds or changes identity")
	}
	w.total += size
	if _, err := w.writer.addBlobBytes(raw, d.MediaType); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "writing metadata blob")
	}
	return nil
}

// Reserve the exact allocation before opening a response and retain the reservation
// through the write. Readers waiting for put therefore still count against the
// shared bound. The extra byte detects a body larger than its declared size.
func (w *metadataBlobWriter) copyBlob(ctx context.Context, d Descriptor, max int64, open func() (io.ReadCloser, error)) error {
	if d.Size < 0 || d.Size > max || d.Size >= maxMetadataReadBytes {
		return e.New(e.CodeVerifyFailed, "oversized metadata blob")
	}
	size := d.Size + 1
	if err := w.inflight.Acquire(ctx, size); err != nil {
		return metadataFetchError(ctx, err, "reserve metadata buffer")
	}
	defer w.inflight.Release(size)
	reader, err := open()
	if err != nil {
		return err
	}
	defer reader.Close()
	raw := make([]byte, size)
	n := 0
	for n < len(raw) {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		read, err := reader.Read(raw[n:])
		n += read
		if err == io.EOF {
			break
		}
		// Preserve a transport's unexpected EOF. io.ReadFull would also
		// synthesize that error for an otherwise complete shorter body.
		if err != nil {
			return metadataFetchError(ctx, err, "read metadata blob")
		}
	}
	return w.put(raw[:n], d, max)
}
