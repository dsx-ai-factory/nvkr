// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Work stays synchronous: output failure cancels work without bypassing its
// child-process joins or cleanup. No renderer, terminal mode or event queue.
func runProgress(ctx context.Context, out io.Writer, work func(context.Context, io.Writer) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &checkedWriter{out: out, cancel: cancel}
	return errors.Join(work(workCtx, writer), writer.failure())
}

type checkedWriter struct {
	mu     sync.Mutex
	out    io.Writer
	err    error
	cancel context.CancelFunc
}

func (w *checkedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = nkderr.New(nkderr.CodePreflightFailed, "progress output failed")
		w.cancel()
		return n, w.err
	}
	return n, nil
}

func (w *checkedWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Only observed operation boundaries are presented. Quoted names cannot emit
// terminal controls; caller-supplied status text and private diagnostics are not
// copied into this output. Build stdout continues to stream through Write.
func (w *checkedWriter) Report(event nkedemo.ProgressEvent) {
	var status string
	switch event.Kind {
	case nkedemo.LaneStarted, nkedemo.PhaseStarted, nkedemo.DeploymentStarted:
		status = "START"
	case nkedemo.LaneFinished, nkedemo.PhaseFinished, nkedemo.DeploymentFinished:
		status = "DONE"
	case nkedemo.LaneFailed, nkedemo.PhaseFailed, nkedemo.DeploymentFailed:
		status = "FAIL"
	default:
		return
	}
	_, _ = fmt.Fprintf(w, "[%s] %q\n", status, event.Name)
}
