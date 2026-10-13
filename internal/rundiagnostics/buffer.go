// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package rundiagnostics retains private console bytes in memory for reporting
// after a terminal session has restored the screen and joined its work. Bytes
// are never safe progress data, persisted diagnostics, or execution authority.
package rundiagnostics

import (
	"bytes"
	"sync"
)

// MaxBytes bounds the retained tail across both producer streams. bytes.Buffer
// may retain a backing allocation larger than its contents, by a constant factor.
const MaxBytes = 128 << 10

// Buffer is ready for use at its zero value. Writers perform only bounded memory
// work, never filesystem I/O, callbacks, or renderer updates. Do not copy it.
type Buffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

// Write drains all supplied bytes, retaining only the most recent MaxBytes.
// Overflow never fails a producer or prevents an owned child from being joined.
func (b *Buffer) Write(raw []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(raw)
	if n > MaxBytes-b.buffer.Len() {
		b.truncated = true
	}
	if n >= MaxBytes {
		b.buffer.Reset()
		raw = raw[n-MaxBytes:]
	} else if discard := b.buffer.Len() + n - MaxBytes; discard > 0 {
		b.buffer.Next(discard)
	}
	_, _ = b.buffer.Write(raw)
	return n, nil
}

// Snapshot returns detached private bytes and a latched truncation indicator.
func (b *Buffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes()), b.truncated
}
