// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/gdamore/tcell/v2"
)

func TestDashboardTerminalEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, terminal                       string
		stdio, plain, verbose, input, output bool
		want                                 bool
	}{
		{"interactive", "xterm-256color", true, false, false, true, true, true},
		{"redirected writer", "xterm", false, false, false, true, true, false},
		{"plain", "xterm", true, true, false, true, true, false},
		{"verbose", "xterm", true, false, true, true, true, false},
		{"unset TERM", "", true, false, false, true, true, false},
		{"dumb", "dumb", true, false, false, true, true, false},
		{"redirected input", "xterm", true, false, false, false, true, false},
		{"redirected stdout", "xterm", true, false, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashboardTerminalEligible(tc.stdio, tc.plain, tc.verbose, tc.terminal, tc.input, tc.output); got != tc.want {
				t.Fatalf("eligibility=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestRecoveryLocatorRejectsEmptyWorkspace(t *testing.T) {
	for _, work := range []string{"", " \t "} {
		ctx, err := progressRecoveryContext(t.Context(), work)
		assertCode(t, err, nvkrerr.CodeUsage)
		if ctx != nil {
			t.Fatal("missing configured path invented a recovery workspace")
		}
	}
}

func TestDashboardRendererPanicRestoresBeforeCleanupJoin(t *testing.T) {
	for _, ownerErr := range []error{nil, nvkrerr.New(nvkrerr.CodeFetchFailed, "owner fetch failed")} {
		t.Run(map[bool]string{false: "cleanup success", true: "cleanup failure"}[ownerErr != nil], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			restored, release, result := make(chan struct{}), make(chan struct{}, 1), make(chan error, 1)
			defer close(release)
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), panicDraw: true, onFini: func() { close(restored) }}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			go func() {
				result <- dashboardLoop(ctx, cancel, screen, func() error { return nil }, writer, view, func(ctx context.Context, _ io.Writer) error {
					<-ctx.Done()
					<-release
					return ownerErr
				})
			}()
			select {
			case <-restored:
			case <-time.After(3 * time.Second):
				t.Fatal("renderer panic kept terminal raw while cleanup was blocked")
			}
			select {
			case <-result:
				t.Fatal("renderer panic returned with unjoined cleanup")
			default:
			}
			// A buffered release also keeps failure-path deferred cleanup safe.
			release <- struct{}{}
			select {
			case err := <-result:
				assertCode(t, err, nvkrerr.CodeInternal)
				if ownerErr != nil && !errors.Is(err, ownerErr) {
					t.Fatalf("renderer panic discarded owner's exact coded error: %v", err)
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatal("private panic escaped presentation boundary")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("renderer panic failed to join released cleanup")
			}
		})
	}
}

func TestDashboardPresentationFailureRestoresBeforeCleanupJoin(t *testing.T) {
	for _, mode := range []string{"output failure", "event error", "closed events"} {
		for _, ownerErr := range []error{nil, nvkrerr.New(nvkrerr.CodeFetchFailed, "owner fetch failed")} {
			t.Run(mode+map[bool]string{false: "/cleanup success", true: "/cleanup failure"}[ownerErr != nil], func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started, restored, pumpJoined := make(chan struct{}), make(chan struct{}), make(chan struct{})
				release, result := make(chan struct{}, 1), make(chan error, 1)
				defer close(release)
				var outputFailed, pumpRestoredFirst atomic.Bool
				presentationErr := nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal output failed")
				screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), closed: mode == "closed events",
					onPumpExit: func() { close(pumpJoined) }, onFini: func() {
						select {
						case <-pumpJoined:
							pumpRestoredFirst.Store(true)
						default:
						}
						close(restored)
					}}
				view := newDashboard()
				writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
				go func() {
					result <- dashboardLoop(ctx, cancel, screen, func() error {
						if outputFailed.Load() {
							return presentationErr
						}
						return nil
					}, writer, view, func(ctx context.Context, _ io.Writer) error {
						close(started)
						<-ctx.Done()
						<-release
						return ownerErr
					})
				}()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("workflow did not start")
				}
				switch mode {
				case "output failure":
					outputFailed.Store(true)
				case "event error":
					if err := screen.PostEvent(tcell.NewEventError(errors.New("private terminal input diagnostic"))); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-restored:
				case <-time.After(3 * time.Second):
					t.Fatal("presentation failure kept terminal raw while cleanup was blocked")
				}
				if !pumpRestoredFirst.Load() || ctx.Err() == nil {
					t.Fatal("terminal restoration preceded pump join or work cancellation")
				}
				select {
				case <-result:
					t.Fatal("presentation failure returned with unjoined cleanup")
				default:
				}
				// The owner may fail during cleanup after the terminal is restored.
				// Its exact error must remain discoverable alongside presentation failure.
				release <- struct{}{}
				select {
				case err := <-result:
					// Presentation caused cancellation; preserve the owner's exact
					// joined failure without replacing the terminal exit category.
					assertCode(t, err, nvkrerr.CodePreflightFailed)
					if ownerErr != nil && !errors.Is(err, ownerErr) || mode == "output failure" && !errors.Is(err, presentationErr) {
						t.Fatalf("exact owner or presentation failure lost: %v", err)
					}
					message := map[string]string{"output failure": "terminal output failed", "event error": "terminal input failed", "closed events": "terminal event stream ended"}[mode]
					if !strings.Contains(err.Error(), message) {
						t.Fatalf("presentation failure collapsed into owner error: %v", err)
					}
					for _, forbidden := range []string{"private", "PASS", "successfully"} {
						if strings.Contains(err.Error(), forbidden) {
							t.Fatalf("private or fabricated successful output: %v", err)
						}
					}
				case <-time.After(3 * time.Second):
					t.Fatal("presentation failure failed to join released cleanup")
				}
			})
		}
	}
}

type failedLifecycleTTY struct{ tcell.Tty }

func (failedLifecycleTTY) Stop() error  { return errors.New("private stop failure") }
func (failedLifecycleTTY) Close() error { return errors.New("private close failure") }

func TestCheckedTTYLifecycleFailureCancelsPrivately(t *testing.T) {
	for _, operation := range []string{"stop", "close"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tty := &checkedTTY{Tty: failedLifecycleTTY{}, cancel: cancel}
			var err error
			if operation == "stop" {
				err = tty.Stop()
			} else {
				err = tty.Close()
			}
			assertCode(t, tty.failure(), nvkrerr.CodePreflightFailed)
			if err == nil || ctx.Err() == nil || strings.Contains(tty.failure().Error(), "private") {
				t.Fatal("TTY lifecycle failure escaped privately or failed to cancel")
			}
		})
	}
}

func TestDashboardRecoveryGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, work string
		command    bool
	}{
		{"unknown", "", false},
		{"ordinary", "/tmp/demo workspace", true},
		{"shell syntax", "/tmp/demo 'quoted' \"$HOME\" `false` \\ path", true},
		{"terminal control", "/tmp/demo\x1b[2J\tpath", false},
		{"non UTF-8", "/tmp/demo\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const binary = "/tmp/nke demo 'binary'"
			guidance := dashboardRecoveryGuidance(tc.work, binary)
			if strings.ContainsRune(guidance, '\x1b') || strings.ContainsRune(guidance, '\t') || strings.Contains(guidance, "PASS") || !strings.Contains(guidance, "not tenant, VM or container deletion") {
				t.Fatalf("unsafe or overstated recovery guidance: %q", guidance)
			}
			const prefix = "If its original network-state.json exists, after inspecting and stopping surviving owned producers: "
			_, argument, found := strings.Cut(guidance, prefix)
			if found != tc.command {
				t.Fatalf("copyable command=%v want=%v: %q", found, tc.command, guidance)
			}
			if found {
				argument, _, _ = strings.Cut(argument, "\n")
				// Exercise the printed shell argument rather than treating a
				// quoting regex as proof. No user data is executed.
				command := exec.CommandContext(t.Context(), "/bin/sh", "-c", "set -- "+argument+"; test \"$#\" -eq 4 && test \"$1\" = \"$RECOVERY_TEST_BINARY\" && test \"$2\" = restore && test \"$3\" = --work && printf '%s' \"$4\"")
				command.Env = []string{"RECOVERY_TEST_BINARY=" + binary}
				output, err := command.Output()
				if err != nil || string(output) != tc.work {
					t.Fatalf("recovery command changes its configured path: %q %v", output, err)
				}
			}
		})
	}
}

func TestCommandRecoveryLocatorUsesSelectedConfiguration(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, operation := range []string{"run", "e2e", "restore"} {
		t.Run(operation, func(t *testing.T) {
			var out bytes.Buffer
			file := configuredFixture()
			file.Run.NVKR, file.Run.Runs = "", nvkedemo.RunFileInputs{}
			if operation == "run" {
				file = configuredFixture()
			}
			work := "selected-output"
			want, err := filepath.Abs(work)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context) {
				if got := progressRecoveryWork(ctx); got != want {
					t.Fatalf("recovery locator=%q want selected=%q", got, want)
				}
			}
			called := false
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) { return file, nil },
				prepare: func(ctx context.Context, _ nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
					check(ctx)
					return cfg, nil
				},
				build: func(ctx context.Context, _ nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					check(ctx)
					return nvkedemo.BuiltInputs{NVKR: "fresh"}, nil
				},
				run: func(ctx context.Context, cfg nvkedemo.Config) error {
					check(ctx)
					called = true
					if cfg.Out != work {
						t.Fatal("presentation changed configured path")
					}
					return nil
				},
				restore: func(ctx context.Context, path string, _ io.Writer) error {
					check(ctx)
					called = true
					if path != work {
						t.Fatal("presentation changed configured path")
					}
					return nil
				},
			}
			flag := "--out"
			if operation == "restore" {
				flag = "--work"
			}
			err = executeRoot(t.Context(), newCommand(&out, io.Discard, actions), []string{operation, "--config", "explicit.yaml", flag, work, "--plain"})
			assertCode(t, err, "")
			if !called {
				t.Fatal("configured command never reached its owner")
			}
		})
	}
}
