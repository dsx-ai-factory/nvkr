// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Plain work stays synchronous; the terminal presenter owns and joins its one
// worker. Neither output mode bypasses child-process joins or cleanup.
func runProgress(ctx context.Context, out io.Writer, plain bool, work func(context.Context, io.Writer) error, auth ...func(context.Context) error) error {
	return presentProgress(ctx, out, nil, false, "", plain, work, auth...)
}

// Allocate a private retained log only after command/configuration admission.
// Deployment captures remain private in their existing owner, not this stream.
func runBuildProgress(ctx context.Context, out io.Writer, verbose, plain bool, work func(context.Context, io.Writer) error, auth ...func(context.Context) error) (result error) {
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
	return presentProgress(ctx, out, log, verbose, log.Name(), plain, work, auth...)
}

func progressOutput(ctx context.Context, out, raw io.Writer, verbose bool, logPath string, work func(context.Context, io.Writer) error, auth ...func(context.Context) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &checkedWriter{out: out, raw: raw, verbose: verbose, remaining: 8 << 20, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	if len(auth) > 0 {
		writer.sudoAuth = auth[0]
	}
	if logPath != "" {
		writer.emit(fmt.Sprintf("Build log (private, retained; maximum 8 MiB): %q\nUse --verbose to stream raw public build output.\n", logPath))
		if err := writer.failure(); err != nil {
			return err
		}
	}
	err := work(workCtx, writer)
	writer.mu.Lock()
	writer.lineOutput()
	if err != nil && writer.failedPhase != "" {
		writer.emit(fmt.Sprintf("\nStopped at: %s\nDiagnostics: inspect the private logs in the retained demo output; build details use the private build log when listed above.\n", terminalText(writer.failedPhase)))
	}
	writer.mu.Unlock()
	return errors.Join(err, writer.failure())
}

type checkedWriter struct {
	mu          sync.Mutex
	out         io.Writer
	err         error
	cancel      context.CancelFunc
	raw         io.Writer
	verbose     bool
	remaining   int
	truncated   bool
	started     time.Time
	phases      map[string]time.Time
	line        []byte
	discard     bool
	failedPhase string
	tenantStage string
	observer    nkedemo.ProgressReporter
	sudoAuth    func(context.Context) error
}

// Escape controls without surrounding ordinary progress text in quoted strings.
func terminalText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func (w *checkedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if observer, ok := w.observer.(interface{ BuildOutput(int) }); ok && len(data) > 0 {
		observer.BuildOutput(len(data))
	}
	if w.raw != nil {
		retained := min(len(data), w.remaining)
		if retained > 0 {
			if err := writeAll(w.raw, data[:retained]); err != nil {
				return 0, w.fail("build log write failed")
			}
			w.remaining -= retained
		}
		if len(data) > retained && !w.truncated {
			w.truncated = true
			w.emit("Build log retention reached 8 MiB; further output is not retained.\n")
			if w.err != nil {
				return 0, w.err
			}
		}
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
	// Owner-generated graph projections use existing JSON records. Keep them
	// out of the activity wall and plain output; only the dashboard consumes
	// them as bounded descriptions. The retained raw log still has the trace.
	for _, prefix := range []string{"Build vertex: ", "Build edge: ", "Build display: "} {
		if strings.HasPrefix(line, prefix) {
			if w.observer != nil {
				w.emit(line + "\n")
			}
			return
		}
	}
	if summary := dashboardGuestReport(line); summary != "" {
		w.emit(fmt.Sprintf("[build +%s] %s%s\n", time.Since(w.started).Truncate(time.Second), dashboardGuestReportPrefix, summary))
		return
	}
	if strings.HasPrefix(line, "==> ") && strings.HasSuffix(line, ": Waiting for SSH to become available...") {
		w.emit(fmt.Sprintf("[build +%s] %s\n", time.Since(w.started).Truncate(time.Second), terminalText(line)))
		return
	}
	// Human subprocess markers are labeled logs, not authenticated Run events.
	for _, prefix := range []string{"[dry-run] ", "[start] ", "[done] ", "[cached] ", "nkdctl command:", "Build graph:", "  output:", "OS advisory", "Prerequisite diagnostics", "Warning:", "Build all artifacts", "Artifact builds complete;", "Deployment is separate:", "Tenant created;", "PASS:", "Unqualified local diagnostic"} {
		if strings.HasPrefix(line, prefix) {
			w.emit(fmt.Sprintf("[build +%s] %s\n", time.Since(w.started).Truncate(time.Second), terminalText(line)))
			return
		}
	}
}

func (w *checkedWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Only observed operation boundaries are presented. Escaped names cannot emit
// terminal controls; caller-supplied status text and private diagnostics are not
// copied into this output. Report does not reparse subprocess log text.
func (w *checkedWriter) Report(event nkedemo.ProgressEvent) {
	if event.Kind == nkedemo.ProcessStarted || event.Kind == nkedemo.ProcessExited {
		if w.observer != nil {
			w.observer.Report(event)
		}
		return
	}
	if event.Kind == nkedemo.TenantWaiting {
		w.tenantStatus(event)
		return
	}
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
	if w.observer != nil {
		w.observer.Report(event)
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
	if status == "FAIL" && w.failedPhase == "" {
		w.failedPhase = event.Name
	}
	// The dashboard consumes these typed observations directly. Repeating every
	// private mechanical step in its activity pane hides meaningful changes.
	// Plain output retains the full trace; failures remain visible in both modes.
	if w.observer == nil || status == "FAIL" {
		w.emit(fmt.Sprintf("[%s %s] %s\n", status, elapsed, terminalText(event.Name)))
	}
}

func (w *checkedWriter) tenantStatus(event nkedemo.ProgressEvent) {
	var message string
	switch event.Name {
	case "tenant-kubeconfig":
		message = "Waiting for NKE to publish tenant kubeconfig; guest boot and node readiness not yet observed"
	case "tenant-api":
		message = "Tenant kubeconfig available; waiting for a successful tenant API node query (not yet Ready)"
	case "tenant-nodes":
		if event.RegisteredNodes < 0 || event.RegisteredNodes > 2 || event.ReadyNodes < 0 || event.ReadyNodes > event.RegisteredNodes {
			return
		}
		message = fmt.Sprintf("Tenant nodes: %d/2 registered, %d/2 Ready; waiting for both control plane and worker", event.RegisteredNodes, event.ReadyNodes)
		if event.RegisteredNodes == 2 && event.ReadyNodes == 2 {
			message = "Tenant nodes: 2/2 registered, 2/2 Ready; workload and DNS checks still required"
		}
	case "tenant-workload":
		message = "Running tenant workload and cluster DNS checks; successful completion still required"
	default:
		return
	}
	if w.observer != nil {
		w.observer.Report(event)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.observer != nil {
		// The live state pane owns the wait reason and budget. A repeated poll
		// is a heartbeat, not an activity event or evidence of progress.
		return
	}
	if w.tenantStage != event.Name {
		w.tenantStage = event.Name
		w.emit("\n  Artifacts → Site → [Tenant] → Workload/DNS → Cleanup\n")
	}
	w.emit(fmt.Sprintf("  Tenant  %s elapsed · %s remaining\n          %s\n", max(time.Duration(0), event.Elapsed).Truncate(time.Second), max(time.Duration(0), event.Remaining).Truncate(time.Second), message))
}
