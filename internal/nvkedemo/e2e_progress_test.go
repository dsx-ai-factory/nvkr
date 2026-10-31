// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test-only presentation: phase observations and parent liveness prove neither
// descendant progress nor successful build/deployment.
type nativeE2EProgress struct {
	mu           sync.Mutex
	out          io.Writer
	started      time.Time
	phase        string
	since        time.Time
	lastOutput   time.Time
	events       chan ProgressEvent
	observations chan string
	// Set once by TestEndToEnd before starting any build or deployment.
	terminalDebug bool
}

type nativeHeartbeatWriter struct {
	bytes.Buffer
	once      sync.Once
	heartbeat chan struct{}
}

func (w *nativeHeartbeatWriter) Write(raw []byte) (int, error) {
	if bytes.Contains(raw, []byte("E2E parent alive:")) {
		w.once.Do(func() { close(w.heartbeat) })
	}
	return w.Buffer.Write(raw)
}

func TestNativeE2EProgressReportsAndJoins(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-parent=%t", cancelParent), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out := &nativeHeartbeatWriter{heartbeat: make(chan struct{})}
			progress, stop := startNativeE2EProgress(ctx, out, time.Millisecond)
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := stop(cleanup); err != nil {
					t.Error(err)
				}
			}()
			progress.Report(ProgressEvent{Kind: PhaseStarted, Name: "build-all", Status: "started"})
			if _, err := progress.Write([]byte("original child output\n")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-out.heartbeat:
			case <-time.After(5 * time.Second):
				t.Fatal("missing parent heartbeat")
			}
			progress.Report(ProgressEvent{Kind: PhaseFailed, Name: "build-all", Status: "failed"})
			progress.Report(ProgressEvent{Kind: BuildPlanSelected, Name: "private-plan-sentinel"})
			reportNativeBootstrapObservation(Runner{Progress: progress}, "Bootstrap phase observation: preflight succeeded\n")
			if cancelParent {
				cancel()
			}
			cleanup, cancelCleanup := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelCleanup()
			if err := stop(cleanup); err != nil {
				t.Fatal(err)
			}
			// Stop joins the only ticker goroutine, so retained bytes are now
			// stable and can be inspected without racing a presentation write.
			raw := out.String()
			for _, want := range []string{"phase-started: build-all", "phase-failed: build-all", "original child output", "parent alive:", "stream-idle=", "child progress unverified", "Bootstrap phase observation: preflight succeeded"} {
				if !strings.Contains(raw, want) {
					t.Fatalf("missing %q in %q", want, raw)
				}
			}
			if strings.Contains(raw, "private-plan-sentinel") {
				t.Fatal("machine plan entered the progress output")
			}
		})
	}
}

func TestNativeE2EProgressBlockedWriterReturnsUnjoined(t *testing.T) {
	for _, trigger := range []string{"heartbeat", "bootstrap observation"} {
		t.Run(trigger, func(t *testing.T) {
			out := &delayedDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
			interval := time.Hour
			if trigger == "heartbeat" {
				interval = time.Millisecond
			}
			progress, stop := startNativeE2EProgress(t.Context(), out, interval)
			if trigger == "bootstrap observation" {
				reportNativeBootstrapObservation(Runner{Progress: progress}, "Bootstrap phase observation: engine_deploy failed\n")
			}
			var release sync.Once
			unblock := func() { release.Do(func() { close(out.release) }) }
			t.Cleanup(func() {
				unblock()
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := stop(cleanup); err != nil {
					t.Error(err)
				}
			})
			select {
			case <-out.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("presentation did not reach the blocking sink")
			}
			reported := make(chan struct{})
			work := privateTestDir(t)
			var cleanupErr error
			go func() {
				defer close(reported)
				// Presentation may drop descriptions under backpressure; it must not
				// block Runner's failed-phase report or its existing ownership cleanup.
				for range 128 {
					progress.Report(ProgressEvent{Kind: PhaseFailed, Name: "build-all", Status: "failed"})
					reportNativeBootstrapObservation(Runner{Progress: progress}, "Bootstrap phase observation: engine_deploy failed\n")
				}
				cleanupErr = (Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}, Progress: progress}).Run(t.Context(), []string{"/bin/sh", "-c", "exit 0"}, "owned-cleanup", time.Second)
			}()
			t.Cleanup(func() {
				unblock()
				select {
				case <-reported:
				case <-time.After(5 * time.Second):
					t.Error("progress reporter did not join after releasing its sink")
				}
			})
			select {
			case <-reported:
			case <-time.After(5 * time.Second):
				t.Fatal("owned cleanup blocked behind presentation output")
			}
			if cleanupErr != nil {
				t.Fatalf("owned cleanup failed: %v", cleanupErr)
			}
			cleanup, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if err := stop(cleanup); !errors.Is(err, ErrUnjoined) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked heartbeat lost incomplete join/deadline: %v", err)
			}
			unblock()
			joined, cancelJoin := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelJoin()
			if err := stop(joined); err != nil {
				t.Fatalf("released heartbeat failed to join: %v", err)
			}
		})
	}
}

func (p *nativeE2EProgress) Write(raw []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(raw) > 0 {
		p.lastOutput = time.Now()
	}
	return p.out.Write(raw)
}

func (p *nativeE2EProgress) Report(event ProgressEvent) {
	// Descriptive events may be dropped; a blocked presenter must never delay
	// the command owner returning an error or joining its processes.
	switch event.Kind {
	case PhaseStarted, PhaseFinished, PhaseFailed, ProcessStarted, ProcessExited:
		select {
		case p.events <- event:
		default:
		}
	}
}

func (p *nativeE2EProgress) nativeBootstrapTerminalEnabled() bool { return p.terminalDebug }

func (p *nativeE2EProgress) observeNativeBootstrap(text string) {
	if len(text) == 0 || len(text) > 32<<10 {
		return
	}
	select {
	case p.observations <- text:
	default:
	}
}

func (p *nativeE2EProgress) report(event ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Kind == PhaseStarted {
		p.phase, p.since = event.Name, time.Now()
	}
	_, _ = fmt.Fprintf(p.out, "E2E %s: %s (%s)\n", event.Kind, event.Name, event.Status)
}

func (p *nativeE2EProgress) heartbeat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.out, "E2E parent alive: elapsed=%s last-started-phase=%s phase-age=%s stream-idle=%s; child progress unverified\n",
		time.Since(p.started).Truncate(time.Second), p.phase, time.Since(p.since).Truncate(time.Second), time.Since(p.lastOutput).Truncate(time.Second))
}

func startNativeE2EProgress(ctx context.Context, out io.Writer, interval time.Duration) (*nativeE2EProgress, func(context.Context) error) {
	ctx, cancel := context.WithCancel(ctx)
	now := time.Now()
	progress := &nativeE2EProgress{out: out, started: now, phase: "admission", since: now, lastOutput: now, events: make(chan ProgressEvent, 64), observations: make(chan string, 8)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				for {
					select {
					case event := <-progress.events:
						progress.report(event)
					case text := <-progress.observations:
						_, _ = progress.Write([]byte(text))
					default:
						return
					}
				}
			case event := <-progress.events:
				progress.report(event)
			case text := <-progress.observations:
				_, _ = progress.Write([]byte(text))
			case <-ticker.C:
				progress.heartbeat()
			}
		}
	}()
	return progress, func(cleanup context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		default:
		}
		select {
		case <-done:
			return nil
		case <-cleanup.Done():
			return fmt.Errorf("%w: E2E heartbeat: %w", ErrUnjoined, cleanup.Err())
		}
	}
}
