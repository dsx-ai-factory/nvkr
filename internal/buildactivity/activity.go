// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package buildactivity carries bounded descriptive OS build-log metadata.
// These records are neither execution receipts nor artifact/readiness evidence.
package buildactivity

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const (
	Version        = 1
	MaxFiles       = 64
	MaxBytes       = 64 << 20
	MaxAgeMillis   = 6 * 60 * 60 * 1000
	MaxRecordBytes = 1024
	// POSIX guarantees PIPE_BUF of at least 512 bytes. One nonblocking write
	// either transfers the complete record atomically or drops this snapshot.
	maxAtomicBytes = 512
)

type Record struct {
	Version        int    `json:"version"`
	Operation      string `json:"operation"`
	Available      bool   `json:"available"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	GrowthObserved bool   `json:"growth_observed"`
	// GrowthAgeMillis is the age at the source snapshot; transport adds only
	// SampleAgeMillis, so a presenter can add the two without double counting.
	GrowthAgeMillis int64 `json:"growth_age_millis"`
	SampleAgeMillis int64 `json:"sample_age_millis"`
}

// Validate checks only the closed diagnostic vocabulary, not source authority.
func (r Record) Validate() error {
	if r.Version != Version || !validOperation(r.Operation) || r.Files < 0 || r.Files > MaxFiles || r.Bytes < 0 || r.Bytes > MaxBytes ||
		r.GrowthAgeMillis < 0 || r.GrowthAgeMillis > MaxAgeMillis || r.SampleAgeMillis < 0 || r.SampleAgeMillis > MaxAgeMillis ||
		(r.Files == 0 && (r.Bytes != 0 || r.GrowthObserved)) ||
		(!r.GrowthObserved && r.GrowthAgeMillis != 0) ||
		(!r.Available && (r.Files != 0 || r.Bytes != 0 || r.GrowthObserved || r.GrowthAgeMillis != 0 || r.SampleAgeMillis != 0)) {
		return e.New(e.CodeUsage, "invalid OS build-log metadata")
	}
	return nil
}

func validOperation(operation string) bool {
	if len(operation) == 0 || len(operation) > 256 {
		return false
	}
	segmentStart := 0
	for i, c := range operation {
		if c == '/' {
			if operation[segmentStart:i] == "" || operation[segmentStart:i] == "." || operation[segmentStart:i] == ".." {
				return false
			}
			segmentStart = i + 1
		} else if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return operation[segmentStart:] != "" && operation[segmentStart:] != "." && operation[segmentStart:] != ".."
}

// Decode uses the existing canonical codec: unknown/duplicate fields, alternate
// bytes, trailing values and unsafe names are never accepted as UI telemetry.
func Decode(raw []byte) (Record, error) {
	var record Record
	if len(raw) == 0 || len(raw) >= MaxRecordBytes {
		return record, e.New(e.CodeUsage, "OS build-log record exceeds its bound")
	}
	if err := canonical.UnmarshalExact(raw, &record); err != nil {
		return Record{}, e.New(e.CodeUsage, "malformed OS build-log metadata")
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Sink owns a nonblocking pipe endpoint. It has no goroutines and calls no
// caller-provided writer. Ordinary loss must never become a build failure.
type Sink struct {
	mu sync.Mutex
	fd int
}

// AdoptFD transfers an explicitly selected writable pipe endpoint. Mark the
// inherited endpoint close-on-exec before any subsequent child can be started.
// The returned duplicate is likewise close-on-exec and is owned until Close.
func AdoptFD(ctx context.Context, fd int) (*Sink, error) {
	if ctx == nil || ctx.Err() != nil || fd < 3 {
		return nil, e.New(e.CodeUsage, "build activity requires a live context and private pipe descriptor")
	}
	descriptorFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return nil, e.New(e.CodeUsage, "cannot inspect build activity descriptor")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, descriptorFlags|unix.FD_CLOEXEC); err != nil {
		return nil, e.New(e.CodeUsage, "cannot isolate build activity descriptor")
	}
	var stat unix.Stat_t
	flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err := unix.Fstat(fd, &stat); err != nil || flagErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return nil, e.New(e.CodeUsage, "build activity requires a writable pipe descriptor")
	}
	duplicate, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, e.New(e.CodeUsage, "cannot adopt build activity pipe")
	}
	if err := unix.SetNonblock(duplicate, true); err != nil {
		_ = unix.Close(duplicate)
		return nil, e.New(e.CodeUsage, "cannot make build activity pipe nonblocking")
	}
	if err := unix.Close(fd); err != nil {
		_ = unix.Close(duplicate)
		return nil, e.New(e.CodeUsage, "cannot close inherited build activity pipe")
	}
	return &Sink{fd: duplicate}, nil
}

// Offer drops invalid, oversized or backpressured snapshots rather than blocking
// execution. Canonical metadata contains no paths, filenames or source text.
func (s *Sink) Offer(record Record) {
	if s == nil || record.Validate() != nil {
		return
	}
	raw, err := canonical.Marshal(record)
	if err != nil || len(raw)+1 > maxAtomicBytes {
		return
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 3 {
		return
	}
	_, err = unix.Write(s.fd, raw)
	if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
		_ = unix.Close(s.fd)
		s.fd = -1
	}
}

func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 3 {
		return nil
	}
	err := unix.Close(s.fd)
	s.fd = -1
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return e.New(e.CodeUsage, "cannot close build activity pipe")
	}
	return nil
}
