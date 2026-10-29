// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationprogress

import (
	"bufio"
	"context"
	"errors"
	"os"
	"sync"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const maxQueuedEvents = 256

// Receiver drains a private pipe into a bounded ordered queue. Its only reader
// goroutine never invokes a presenter. Loss affects presentation, never authority.
type Receiver struct {
	file      *os.File
	done      chan struct{}
	mu        sync.Mutex
	events    []Event
	closeOnce sync.Once
}

func NewReceiver(ctx context.Context, file *os.File) (*Receiver, error) {
	if ctx == nil || ctx.Err() != nil || file == nil {
		return nil, e.New(e.CodeUsage, "qualification progress receiver requires a live context and owned pipe")
	}
	if info, err := file.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, e.New(e.CodeUsage, "qualification progress receiver requires an owned pipe")
	}
	r := &Receiver{file: file, done: make(chan struct{})}
	go r.read()
	return r, nil
}

func (r *Receiver) read() {
	defer close(r.done)
	reader := bufio.NewReaderSize(r.file, MaxRecordBytes)
	oversized := false
	for {
		raw, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			oversized = true
			continue // drain without retaining unbounded bytes
		}
		if err != nil {
			return // a truncated frame cannot become an observation
		}
		if oversized {
			oversized = false
			continue
		}
		event, decodeErr := Decode(raw[:len(raw)-1])
		if decodeErr != nil {
			continue
		}
		r.mu.Lock()
		if len(r.events) == maxQueuedEvents {
			// Keep the latest bounded history, preserving its original order.
			copy(r.events, r.events[1:])
			r.events[len(r.events)-1] = event
		} else {
			r.events = append(r.events, event)
		}
		r.mu.Unlock()
	}
}

// Drain is called only by the workflow owner, keeping presenter calls serial.
func (r *Receiver) Drain() []Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events
	r.events = nil
	return events
}

// Finish preserves the final frames after the child and its group have joined.
// If an inherited endpoint remains open, bounded best-effort telemetry is closed
// and joined; missing progress cannot turn the operation into success.
func (r *Receiver) Finish(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return e.New(e.CodeTimeout, "qualification progress reader join requires a context")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-ctx.Done():
	case <-timer.C:
	}
	return r.Close(ctx)
}

// Close interrupts the pipe and joins its sole reader even on canceled work.
func (r *Receiver) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return e.New(e.CodeTimeout, "qualification progress reader join requires a context")
	}
	r.closeOnce.Do(func() { _ = r.file.Close() })
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-r.done:
		return nil
	case <-timer.C:
		return e.New(e.CodeTimeout, "qualification progress reader did not join")
	}
}
