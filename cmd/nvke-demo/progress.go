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

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "build progress canceled")
	}
	log, err := os.CreateTemp("", "nvke-demo-build-*.log")
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "create private build log")
	}
	defer func() {
		if err := log.Close(); err != nil {
			result = errors.Join(result, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "close build log"))
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
	err := runProgressWork(workCtx, writer, work)
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
	observer    nvkedemo.ProgressReporter
	sudoAuth    func(context.Context) error
	// One bounded latest line per public result category, independent of the
	// dashboard's activity ring. Success statements replay only after success.
	results              [8]string
	operatorStatus       string
	walkthroughStatus    string
	retainedInstructions string
	guests               [3]nvkedemo.TenantGuestObservation
	guestsReported       bool
}

// Escape controls without surrounding ordinary progress text in quoted strings.
func terminalText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func (w *checkedWriter) displayText(value string) string {
	if _, dashboardOutput := w.out.(*dashboard); dashboardOutput {
		// The dashboard's bounded activity boundary escapes raw text exactly
		// once, including direct writes and partial lines.
		return value
	}
	return terminalText(value)
}

func (w *checkedWriter) retainResult(line string) {
	index := -1
	switch {
	case strings.HasPrefix(line, "Artifact builds complete;"), strings.HasPrefix(line, "Native catalog tools verified and prepared:"):
		index = 0
	case strings.HasPrefix(line, "Deployment is separate:"):
		index = 1
	case strings.HasPrefix(line, "PASS:"), strings.HasPrefix(line, "PASS (retained):"), strings.HasPrefix(line, "PASS (NVIDIA OVN):"),
		strings.HasPrefix(line, "PASS (upstream OVN diagnostic; NVIDIA OVN not validated):"),
		strings.HasPrefix(line, "PASS (Kind/Calico diagnostic; NVIDIA OVN not deployed):"):
		index = 2
	case strings.HasPrefix(line, "Unqualified local diagnostic"):
		index = 3
	case strings.HasPrefix(line, "Artifacts Run:"):
		index = 4
	case strings.HasPrefix(line, "Native tools Run:"):
		index = 5
	case strings.HasPrefix(line, "Warning:"):
		index = 6
	case strings.HasPrefix(line, "OS advisory"):
		index = 7
	}
	if index >= 0 {
		text := []rune(terminalText(line))
		if len(text) > dashboardRunes {
			text = append(text[:dashboardRunes-1], '…')
		}
		w.results[index] = string(text)
	}
}

// Called with w.mu held, after the screen has restored scrolling output.
func (w *checkedWriter) replayResults(success bool) {
	if w.retainedInstructions != "" {
		w.emit(w.retainedInstructions)
	}
	if w.walkthroughStatus != "" {
		w.emit("\nRecorded walkthrough observed before cleanup; historical observations.\n" + w.walkthroughStatus)
	}
	if w.operatorStatus != "" {
		w.emit("\nCluster snapshot observed during this invocation; historical, not a claim that services remain running.\n" + w.operatorStatus)
	}
	for index, line := range w.results {
		if line != "" && (success || index >= 6) {
			w.emit(line + "\n")
		}
	}
}

// Preserve the complete owner-issued recovery/access commands after terminal
// restoration, including a canceled presenter or a failed deployment.
func (w *checkedWriter) WriteRetainedInstructions(instructions nvkedemo.RetainedInstructions) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	text := instructions.Text()
	if text == "" || len(text) > 64<<10 {
		return w.fail("retained instructions require a bounded owner-generated presentation")
	}
	w.retainedInstructions = text
	w.emit(text)
	return w.err
}

// The owner has already redacted and validated these rows. Do not send them
// through the subprocess-log whitelist or parse table strings for authority.
// Keep the bounded snapshot outside the activity ring for post-terminal replay,
// even when subsequent cleanup fails. Plain output receives it immediately.
func (w *checkedWriter) WriteOperatorStatus(status nvkedemo.OperatorStatus) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	return w.writeOperatorPresentation(status.Text(), status.Walkthrough())
}

// Called under w.mu after the owner-issued status has crossed the public seam.
func (w *checkedWriter) writeOperatorPresentation(text string, walkthrough bool) error {
	if text == "" {
		return w.fail("cluster presentation requires a bounded owner-generated observation")
	}
	if len(text) > 64<<10 {
		return w.fail("cluster presentation exceeds its bounded observation size")
	}
	if walkthrough {
		if len(w.walkthroughStatus)+len(text) > 96<<10 {
			return w.fail("recorded walkthrough exceeds its bounded presentation size")
		}
		w.walkthroughStatus += text
	} else {
		w.operatorStatus = text
	}
	if dashboard, ok := w.out.(*dashboard); ok {
		dashboard.mu.Lock()
		dashboard.operatorPage = ""
		if walkthrough {
			dashboard.operatorPage = text
		}
		dashboard.mu.Unlock()
	}
	w.emit(text)
	return w.err
}

func (w *checkedWriter) OperatorStatusPageDuration() time.Duration {
	if _, ok := w.out.(*dashboard); ok {
		return 3 * time.Second
	}
	return 0
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
		w.err = nvkrerr.New(nvkrerr.CodePreflightFailed, "%s", message)
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
	if summary := dashboardGuestReport(line); summary != "" {
		w.emit(fmt.Sprintf("[log +%s] %s%s\n", time.Since(w.started).Truncate(time.Second), dashboardGuestReportPrefix, summary))
		return
	}
	if strings.HasPrefix(line, "==> ") && strings.HasSuffix(line, ": Waiting for SSH to become available...") {
		w.emit(fmt.Sprintf("[log +%s] %s\n", time.Since(w.started).Truncate(time.Second), w.displayText(line)))
		return
	}
	// Human subprocess markers are labeled logs, not authenticated Run events.
	for _, prefix := range []string{"[dry-run] ", "[start] ", "[done] ", "[cached] ", "nvkr command:", "Build graph:", "  output:", "OS advisory", "Prerequisite diagnostics", "Warning:", "Build all artifacts", "Artifact builds complete;", "Artifacts Run:", "Native tools Run:", "Native catalog tools verified and prepared:", "Deployment is separate:", "Tenant created;", "PASS:", "PASS (retained):", "PASS (NVIDIA OVN):", "PASS (upstream OVN diagnostic; NVIDIA OVN not validated):", "PASS (Kind/Calico diagnostic; NVIDIA OVN not deployed):", "Unqualified local diagnostic"} {
		if strings.HasPrefix(line, prefix) {
			w.retainResult(line)
			w.emit(fmt.Sprintf("[log +%s] %s\n", time.Since(w.started).Truncate(time.Second), w.displayText(line)))
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
func (w *checkedWriter) Report(event nvkedemo.ProgressEvent) {
	if event.Kind == nvkedemo.BuildPlanSelected {
		w.mu.Lock()
		w.lineOutput()
		w.discard = false
		w.mu.Unlock()
		if w.observer != nil {
			w.observer.Report(event)
		}
		return
	}
	if event.Kind == nvkedemo.NativeDeployment {
		if w.observer != nil {
			w.observer.Report(event)
		}
		return
	}
	if event.Kind == nvkedemo.OSBuildLogs {
		if event.BuildLogs.Validate() == nil && w.observer != nil {
			w.observer.Report(event)
		}
		// No additional wall of periodic metadata in plain output. The private
		// typed channel is presentation-only, never producer stdout or evidence.
		return
	}
	if event.Kind == nvkedemo.TenantGuests {
		if !dashboardValidGuests(event.Guests) {
			return
		}
		if w.observer != nil {
			w.observer.Report(event)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		for index, role := range tenantGuestRoles(event.ExpectedNodes) {
			guest, previous := event.Guests[index], w.guests[index]
			changed := !w.guestsReported || guest.Available != previous.Available || guest.Domain != previous.Domain || guest.ProviderReady != previous.ProviderReady || guest.ProvisioningFailure != previous.ProvisioningFailure || guest.InstanceState != previous.InstanceState || guest.ConsoleKnown != previous.ConsoleKnown
			growth := guest.Available && previous.Available && guest.ConsoleKnown && previous.ConsoleKnown && guest.ConsoleChanged && guest.ConsoleBytes > previous.ConsoleBytes
			if !changed && !growth {
				continue
			}
			prefix := fmt.Sprintf("  Tenant guest +%s · %s: ", max(time.Duration(0), time.Since(w.started)).Truncate(time.Second), role)
			if guest.ProvisioningFailure == nvkedemo.TenantInstanceCreateFailed {
				w.emit(prefix + "instance lookup/create failed (provider report; not a Kubernetes readiness result)\n")
				continue
			}
			if !guest.Available {
				w.emit(prefix + "guest observation unavailable\n")
				continue
			}
			w.emit(fmt.Sprintf("%sNICo %s; Infra Ready=%s; VM %s; console observed=%t (not Kubernetes Ready)", prefix, guest.InstanceState, guest.ProviderReady, guest.Domain, guest.ConsoleKnown))
			if growth {
				w.emit(fmt.Sprintf("; console bytes grew to %d KiB; growth age %s at snapshot (not guest health)", guest.ConsoleBytes/1024, guest.ConsoleLastChange.Truncate(time.Second)))
			}
			w.emit("\n")
		}
		w.guests, w.guestsReported = event.Guests, true
		return
	}
	if event.Kind == nvkedemo.ProcessStarted || event.Kind == nvkedemo.ProcessExited {
		if w.observer != nil {
			w.observer.Report(event)
		}
		return
	}
	if event.Kind == nvkedemo.TenantWaiting {
		w.tenantStatus(event)
		return
	}
	var status string
	switch event.Kind {
	case nvkedemo.LaneStarted, nvkedemo.PhaseStarted, nvkedemo.DeploymentStarted:
		status = "START"
	case nvkedemo.LaneFinished, nvkedemo.PhaseFinished, nvkedemo.DeploymentFinished:
		status = "DONE"
	case nvkedemo.LaneFailed, nvkedemo.PhaseFailed, nvkedemo.DeploymentFailed:
		status = "FAIL"
	default:
		return
	}
	// A workflow boundary resumes the live phase view even when a failed
	// walkthrough never reaches its final live snapshot. Background metadata
	// returns above and cannot dismiss a page during its display interval.
	// The independent walkthroughStatus buffer still owns historical replay.
	if dashboard, ok := w.out.(*dashboard); ok {
		dashboard.mu.Lock()
		dashboard.operatorPage = ""
		dashboard.mu.Unlock()
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
		w.emit(fmt.Sprintf("[%s %s] %s\n", status, elapsed, w.displayText(event.Name)))
	}
}

func requestedTenantNodes(expected int) int {
	if expected == 0 {
		return 2
	}
	if expected == 2 || expected == 3 {
		return expected
	}
	return 0
}

func tenantGuestRoles(expected int) []string {
	if requestedTenantNodes(expected) == 3 {
		return []string{"Control plane", "Worker 1", "Worker 2"}
	}
	return []string{"Control plane", "Worker"}
}

func (w *checkedWriter) tenantStatus(event nvkedemo.ProgressEvent) {
	expected := requestedTenantNodes(event.ExpectedNodes)
	if expected == 0 {
		return
	}
	var message string
	switch event.Name {
	case "tenant-kubeconfig":
		message = "Waiting for NVKE to publish tenant kubeconfig; guest boot and node readiness not yet observed"
	case "tenant-api":
		message = "Tenant kubeconfig available; waiting for a successful tenant API node query (not yet Ready)"
	case "tenant-nodes":
		if event.RegisteredNodes < 0 || event.RegisteredNodes > expected || event.ReadyNodes < 0 || event.ReadyNodes > event.RegisteredNodes {
			return
		}
		message = fmt.Sprintf("Tenant nodes: %d/%d registered, %d/%d Ready; waiting for control plane and workers", event.RegisteredNodes, expected, event.ReadyNodes, expected)
		if event.RegisteredNodes == expected && event.ReadyNodes == expected {
			message = fmt.Sprintf("Tenant nodes: %d/%d registered, %d/%d Ready; workload and DNS checks still required", expected, expected, expected, expected)
		}
	case "tenant-cloud-init":
		message = "Checking actual host cloud-init completion on the Ready tenant nodes"
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

func nativePresentation(progress io.Writer, conformance bool) {
	if reporter, ok := progress.(nvkedemo.ProgressReporter); ok {
		reporter.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: conformance})
	}
}
