// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"io"
	"sync"
)

// progressSink keeps short lifecycle messages live even while each node's detailed
// log remains privately buffered for non-interleaved output. The scheduler and
// concurrently running build nodes share it, so every write is serialized.
type progressSink struct {
	mu sync.Mutex
	w  io.Writer
}

func (p *progressSink) printf(format string, args ...any) {
	if p == nil || p.w == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.w, format, args...)
}
