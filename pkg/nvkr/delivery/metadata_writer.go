// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Serialize integrity accounting and writes: repeated configs may share a blob
// path even when their owners are fetched concurrently. Limits count acquisition
// bytes, including repeats, just as the sequential pull did.
type metadataBlobWriter struct {
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
