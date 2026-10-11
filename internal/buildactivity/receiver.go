// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildactivity

import (
	"bufio"
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const maxOperations = 64

// Receiver drains one owned pipe into bounded latest-per-operation metadata.
// Its reader never calls a presenter or other caller-controlled callback.
type Receiver struct {
	file      *os.File
	done      chan struct{}
	mu        sync.Mutex
	latest    map[string]receivedRecord
	known     map[string]struct{}
	closeOnce sync.Once
}

type receivedRecord struct {
	record   Record
	received time.Time
}

func NewReceiver(ctx context.Context, file *os.File) (*Receiver, error) {
	if ctx == nil || ctx.Err() != nil || file == nil {
		return nil, e.New(e.CodeUsage, "build activity receiver requires a live context and owned pipe")
	}
	if info, err := file.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, e.New(e.CodeUsage, "build activity receiver requires an owned pipe")
	}
	r := &Receiver{file: file, done: make(chan struct{}), latest: make(map[string]receivedRecord), known: make(map[string]struct{})}
	go r.read()
	return r, nil
}

func (r *Receiver) invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for operation := range r.known {
		r.latest[operation] = receivedRecord{record: Record{Version: Version, Operation: operation}}
	}
}

func (r *Receiver) read() {
	defer close(r.done)
	reader := bufio.NewReaderSize(r.file, MaxRecordBytes)
	oversized := false
	for {
		raw, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			oversized = true
			r.invalidate()
			continue // drain this oversized frame without retaining its bytes
		}
		if err != nil {
			// EOF, truncation or closure makes any previous report unavailable.
			r.invalidate()
			return
		}
		if oversized {
			oversized = false
			continue
		}
		record, decodeErr := Decode(raw[:len(raw)-1])
		if decodeErr != nil {
			r.invalidate()
			continue
		}
		r.mu.Lock()
		if _, exists := r.known[record.Operation]; exists || len(r.known) < maxOperations {
			r.known[record.Operation] = struct{}{}
			r.latest[record.Operation] = receivedRecord{record: record, received: time.Now()}
		}
		r.mu.Unlock()
	}
}

// Drain is called by the existing workflow goroutine, not the pipe reader.
func (r *Receiver) Drain() []Record {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.latest))
	for operation := range r.latest {
		keys = append(keys, operation)
	}
	sort.Strings(keys)
	result := make([]Record, 0, len(keys))
	for _, operation := range keys {
		snapshot := r.latest[operation]
		record := snapshot.record
		if record.Available {
			elapsed := min(time.Since(snapshot.received).Milliseconds(), int64(MaxAgeMillis))
			record.SampleAgeMillis = min(record.SampleAgeMillis+elapsed, int64(MaxAgeMillis))
		}
		result = append(result, record)
		delete(r.latest, operation)
	}
	return result
}

// Close interrupts a pipe read and joins the sole reader before returning.
func (r *Receiver) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return e.New(e.CodeTimeout, "build activity reader join requires a context")
	}
	r.closeOnce.Do(func() { _ = r.file.Close() })
	select {
	case <-r.done:
		return nil
	default:
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return e.Wrap(e.CodeTimeout, ctx.Err(), "build activity reader did not join")
	case <-timer.C:
		return e.New(e.CodeTimeout, "build activity reader did not join")
	}
}
