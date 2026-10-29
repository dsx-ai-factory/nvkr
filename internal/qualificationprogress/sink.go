// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationprogress

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// POSIX guarantees atomic nonblocking pipe writes of at least 512 bytes.
const maxAtomicBytes = 512

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
		return nil, e.New(e.CodeUsage, "qualification progress requires a live context and private pipe descriptor")
	}
	descriptorFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return nil, e.New(e.CodeUsage, "cannot inspect qualification progress descriptor")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, descriptorFlags|unix.FD_CLOEXEC); err != nil {
		return nil, e.New(e.CodeUsage, "cannot isolate qualification progress descriptor")
	}
	var stat unix.Stat_t
	flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err := unix.Fstat(fd, &stat); err != nil || flagErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return nil, e.New(e.CodeUsage, "qualification progress requires a writable pipe descriptor")
	}
	duplicate, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, e.New(e.CodeUsage, "cannot adopt qualification progress pipe")
	}
	if err := unix.SetNonblock(duplicate, true); err != nil {
		_ = unix.Close(duplicate)
		return nil, e.New(e.CodeUsage, "cannot make qualification progress pipe nonblocking")
	}
	if err := unix.Close(fd); err != nil {
		_ = unix.Close(duplicate)
		return nil, e.New(e.CodeUsage, "cannot close inherited qualification progress pipe")
	}
	return &Sink{fd: duplicate}, nil
}

// Offer drops invalid, oversized or backpressured observations rather than
// blocking qualification. Events contain no raw process diagnostics.
func (s *Sink) Offer(event Event) {
	if s == nil || event.Validate() != nil {
		return
	}
	raw, err := canonical.Marshal(event)
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
		return e.New(e.CodeUsage, "cannot close qualification progress pipe")
	}
	return nil
}
