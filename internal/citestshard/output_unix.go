// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type pipeOutput struct {
	ctx  context.Context
	file *os.File
}

func (w pipeOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	if cause := w.ctx.Err(); cause != nil {
		return n, cause
	}
	return n, err
}

// commandOutput borrows file output exclusively until cleanup. A private,
// pollable pipe descriptor lets cancellation interrupt Write without closing
// the caller's descriptor or leaving a forwarding goroutine behind. Non-file
// writers are in-memory test sinks and must not block.
func commandOutput(ctx context.Context, writer io.Writer) (io.Writer, func() error, error) {
	file, ok := writer.(*os.File)
	if !ok {
		return writer, func() error { return nil }, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return writer, func() error { return nil }, nil
	}
	fd := file.Fd()
	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		return nil, nil, err
	}
	duplicate, err := unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetNonblock(duplicate, true); err != nil {
		_ = unix.Close(duplicate)
		return nil, nil, err
	}
	owned := os.NewFile(uintptr(duplicate), "ci-shard-output")
	release := func() error {
		// Duplicated descriptors share status flags; restore the borrowed state.
		_, restoreErr := unix.FcntlInt(uintptr(duplicate), unix.F_SETFL, flags)
		return errors.Join(restoreErr, owned.Close())
	}
	if err := owned.SetWriteDeadline(time.Time{}); err != nil {
		return nil, nil, errors.Join(err, release())
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = owned.SetWriteDeadline(time.Now())
		close(joined)
	})
	return pipeOutput{ctx, owned}, func() error {
		if !stop() {
			<-joined
		}
		return release()
	}, nil
}
