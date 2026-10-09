// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Work stays synchronous: output failure cancels work without bypassing its
// child-process joins or cleanup. No renderer, terminal mode or event queue.
func runProgress(ctx context.Context, out io.Writer, work func(context.Context, io.Writer) error) error {
	return progressOutput(ctx, out, nil, false, "", work)
}

// Allocate a private retained log only after command/configuration admission.
// Deployment captures remain private in their existing owner, not this stream.
func runBuildProgress(ctx context.Context, out io.Writer, verbose bool, work func(context.Context, io.Writer) error) (result error) {
	if err := ctx.Err(); err != nil {
		return nkderr.Wrap(nkderr.CodeTimeout, err, "build progress canceled")
	}
	log, err := os.CreateTemp("", "nke-demo-build-*.log")
	if err != nil {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "create private build log")
	}
	defer func() {
		if err := log.Close(); err != nil {
			result = errors.Join(result, nkderr.Wrap(nkderr.CodePreflightFailed, err, "close build log"))
		}
	}()
	return progressOutput(ctx, out, log, verbose, log.Name(), work)
}

func progressOutput(ctx context.Context, out, raw io.Writer, verbose bool, logPath string, work func(context.Context, io.Writer) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &checkedWriter{out: out, raw: raw, verbose: verbose, remaining: 8 << 20, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	if logPath != "" {
		writer.emit(fmt.Sprintf("Build log (private, retained; maximum 8 MiB): %q\nUse --verbose to stream raw public build output.\n", logPath))
		if err := writer.failure(); err != nil {
			return err
		}
	}
	err := work(workCtx, writer)
	writer.mu.Lock()
	writer.lineOutput()
	writer.mu.Unlock()
	return errors.Join(err, writer.failure())
}

type checkedWriter struct {
	mu        sync.Mutex
	out       io.Writer
	err       error
	cancel    context.CancelFunc
	raw       io.Writer
	verbose   bool
	remaining int
	started   time.Time
	phases    map[string]time.Time
	line      []byte
	discard   bool
}

func (w *checkedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.raw != nil {
		if len(data) > w.remaining {
			return 0, w.fail("build log exceeds 8 MiB bound")
		}
		if err := writeAll(w.raw, data); err != nil {
			return 0, w.fail("build log write failed")
		}
		w.remaining -= len(data)
	}
	if w.verbose {
		w.emit(string(data))
	} else {
		for _, b := range data {
			if b == '\n' {
				w.lineOutput()
				w.discard = false
			} else if !w.discard {
				if len(w.line) == 16<<10 {
					w.line = w.line[:0]
					w.discard = true
				} else {
					w.line = append(w.line, b)
				}
			}
		}
	}
	return len(data), w.err
}

func writeAll(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
func (w *checkedWriter) fail(message string) error {
	if w.err == nil {
		w.err = nkderr.New(nkderr.CodePreflightFailed, "%s", message)
		w.cancel()
	}
	return w.err
}
func (w *checkedWriter) emit(message string) {
	if w.err == nil {
		if err := writeAll(w.out, []byte(message)); err != nil {
			_ = w.fail("progress output failed")
		}
	}
}
func (w *checkedWriter) lineOutput() {
	line := strings.TrimSuffix(string(w.line), "\r")
	w.line = w.line[:0]
	if w.discard || line == "" {
		return
	}
	if strings.HasPrefix(line, "==> ") && strings.HasSuffix(line, ": Waiting for SSH to become available...") {
		w.emit(fmt.Sprintf("[log +%s] %q\n", time.Since(w.started).Truncate(time.Second), line))
		return
	}
	// Human subprocess markers are labeled logs, not authenticated Run events.
	for _, prefix := range []string{"[start] ", "[done] ", "[cached] ", "nkdctl command:", "Build graph:", "  output:", "OS advisory", "Prerequisite diagnostics", "Warning:", "Build all artifacts", "Artifact builds complete;", "Deployment is separate:", "Tenant created;", "PASS:", "Unqualified local diagnostic"} {
		if strings.HasPrefix(line, prefix) {
			w.emit(fmt.Sprintf("[log +%s] %q\n", time.Since(w.started).Truncate(time.Second), line))
			return
		}
	}
}

func (w *checkedWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Only observed operation boundaries are presented. Quoted names cannot emit
// terminal controls; caller-supplied status text and private diagnostics are not
// copied into this output. Report does not reparse subprocess log text.
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
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(w.started).Truncate(time.Second)
	if status == "START" {
		w.phases[event.Name] = now
	} else if start, ok := w.phases[event.Name]; ok {
		elapsed = now.Sub(start).Truncate(time.Second)
		delete(w.phases, event.Name)
	}
	w.emit(fmt.Sprintf("[%s %s] %q\n", status, elapsed, event.Name))
}
