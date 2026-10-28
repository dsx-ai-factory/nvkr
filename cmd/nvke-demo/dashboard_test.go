// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/gdamore/tcell/v2"
)

type testDashboardScreen struct {
	tcell.SimulationScreen
	closed, initFailure, panicDraw bool
	panicDrawAfter, draws          int
	joined                         atomic.Bool
	lastFrame                      string
	onFini                         func()
	onPumpExit                     func()
	onShow                         func()
}

func (s *testDashboardScreen) Init() error {
	if s.initFailure {
		return errors.New("private terminal details")
	}
	err := s.SimulationScreen.Init()
	s.SetSize(140, 48)
	return err
}

func (s *testDashboardScreen) Fini() {
	s.joined.Store(true)
	s.SimulationScreen.Fini()
	if s.onFini != nil {
		s.onFini()
	}
}

func (s *testDashboardScreen) Show() {
	s.SimulationScreen.Show()
	cells, width, _ := s.GetContents()
	var frame strings.Builder
	for index, cell := range cells {
		if index > 0 && index%width == 0 {
			frame.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			frame.WriteRune(cell.Runes[0])
		} else {
			frame.WriteByte(' ')
		}
	}
	s.lastFrame = frame.String()
	if s.onShow != nil {
		s.onShow()
	}
}

func (s *testDashboardScreen) Clear() {
	s.draws++
	if s.panicDraw || s.panicDrawAfter > 0 && s.draws >= s.panicDrawAfter {
		panic("private renderer panic")
	}
	s.SimulationScreen.Clear()
}

func TestDashboardFinalDrawPanicPreservesJoinedWorkerError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code nvkrerr.Code
	}{
		{"failed worker", nvkrerr.New(nvkrerr.CodeFetchFailed, "worker fetch failed"), nvkrerr.CodeFetchFailed},
		{"successful worker", nil, nvkrerr.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), panicDrawAfter: 2}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			err := dashboardLoop(ctx, cancel, screen, func() error { return nil }, writer, view, func(context.Context, io.Writer) error {
				return tc.err
			})
			assertCode(t, err, tc.code)
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("final rendering panic discarded joined owner failure: %v", err)
			}
			if !screen.joined.Load() || !strings.Contains(err.Error(), string(nvkrerr.CodeInternal)) || !strings.Contains(err.Error(), "terminal renderer panicked") {
				t.Fatalf("rendering failure missing or terminal not restored: %v", err)
			}
		})
	}
}

func TestDashboardResultsSurviveTerminalHandover(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		fail         bool
	}{
		{"build", "Artifact builds complete; nvkr: /tmp/demo/nvkr", false},
		{"default OVN", "PASS (NVIDIA OVN): owner result", false},
		{"upstream diagnostic", "PASS (upstream OVN diagnostic; NVIDIA OVN not validated): owner result", false},
		{"Calico diagnostic", "PASS (Kind/Calico diagnostic; NVIDIA OVN not deployed): owner result", false},
		{"restore", "PASS: recorded controls restored", false},
		{"no PASS on error", "PASS: must not replay", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			err := dashboardLoop(ctx, cancel, screen, func() error { return nil }, writer, view, func(_ context.Context, output io.Writer) error {
				_, _ = io.WriteString(output, tc.result+"\nWarning: latest public warning\n")
				for range dashboardLines + 1 {
					_, _ = io.WriteString(output, "[done] image/build\n")
				}
				_, _ = io.WriteString(output, "private raw compiler noise\n")
				if tc.fail {
					return nvkrerr.New(nvkrerr.CodeBuildFailed, "owner failed")
				}
				return nil
			})
			if tc.fail {
				assertCode(t, err, nvkrerr.CodeBuildFailed)
			} else if err != nil {
				t.Fatalf("successful worker failed: %v", err)
			}
			if !screen.joined.Load() {
				t.Fatal("summary handover preceded terminal restoration")
			}
			var out bytes.Buffer
			if got := finishDashboardProgress(writer, &out, "", err, nil); !errors.Is(got, err) {
				t.Fatalf("handover lost worker result: %v", got)
			}
			text := out.String()
			if strings.Contains(text, tc.result) == tc.fail || !strings.Contains(text, "Warning: latest public warning") || strings.Contains(text, "private raw compiler") || strings.ContainsRune(text, '\x1b') {
				t.Fatalf("result handover lost public result or leaked raw output: %q", text)
			}
		})
	}
}

func TestDashboardTerminalFailureAppearsOnceAfterTeardown(t *testing.T) {
	for _, mode := range []string{"initial", "output", "teardown"} {
		for _, ownerErr := range []error{nil, nvkrerr.New(nvkrerr.CodeFetchFailed, "owner fetch failed")} {
			t.Run(mode+map[bool]string{false: "/success", true: "/owner failure"}[ownerErr != nil], func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				failure := nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal sentinel failed")
				var failed atomic.Bool
				failed.Store(mode == "initial")
				screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), onFini: func() { failed.Store(true) }}
				terminalFailure := func() error {
					if failed.Load() {
						return failure
					}
					return nil
				}
				view := newDashboard()
				writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
				started := false
				err := dashboardLoop(ctx, cancel, screen, terminalFailure, writer, view, func(workCtx context.Context, _ io.Writer) error {
					started = true
					if mode == "output" {
						failed.Store(true)
						<-workCtx.Done()
					}
					return ownerErr
				})
				var out bytes.Buffer
				err = finishDashboardProgress(writer, &out, "", err, terminalFailure)
				want := nvkrerr.CodePreflightFailed
				if started && ownerErr != nil && mode != "output" {
					want = nvkrerr.CodeFetchFailed
				}
				assertCode(t, err, want)
				if !screen.joined.Load() || !errors.Is(err, failure) || started && ownerErr != nil && !errors.Is(err, ownerErr) || strings.Count(err.Error(), "terminal sentinel failed") != 1 {
					t.Fatalf("terminal identity repeated/lost, owner lost or teardown unjoined: %v", err)
				}
				if strings.Contains(out.String(), "returned successfully") {
					t.Fatal("known terminal failure printed successful completion")
				}
			})
		}
	}
}

func TestDashboardCompletionBannerDoesNotPromiseAbsentInstructions(t *testing.T) {
	var out bytes.Buffer
	writer := &checkedWriter{out: &out, started: time.Now()}
	if err := finishDashboardProgress(writer, &out, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "returned successfully") || strings.Contains(out.String(), "instructions below") {
		t.Fatal("generic command banner promised an absent retained handoff")
	}
}

func TestDashboardFinalShowFailurePreservesJoinedOwnerFailure(t *testing.T) {
	for _, ownerErr := range []error{nil, nvkrerr.New(nvkrerr.CodeFetchFailed, "owner fetch failed before final draw")} {
		t.Run(map[bool]string{true: "failed owner", false: "successful owner"}[ownerErr != nil], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var failed atomic.Bool
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			screen.onShow = func() {
				if screen.draws >= 2 {
					failed.Store(true)
				}
			}
			failure := nvkrerr.New(nvkrerr.CodePreflightFailed, "final terminal show failed")
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			err := dashboardLoop(ctx, cancel, screen, func() error {
				if failed.Load() {
					return failure
				}
				return nil
			}, writer, view, func(context.Context, io.Writer) error { return ownerErr })
			assertCode(t, err, nvkrerr.CodePreflightFailed)
			if !screen.joined.Load() || !errors.Is(err, failure) || ownerErr != nil && !errors.Is(err, ownerErr) {
				t.Fatal("final output failure lost its joined owner result or terminal failure")
			}
		})
	}
}

func TestDashboardCleanupNoticeFailureRemainsCheckedAndJoined(t *testing.T) {
	for _, test := range []struct {
		name   string
		writer io.Writer
	}{
		{"short write", shortTTY{}},
		{"write error", failedProgressWriter{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), closed: true}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			var cleaned atomic.Bool
			err := dashboardLoopWithCleanupNotice(ctx, cancel, screen, func() error { return nil }, writer, view, func(workCtx context.Context, _ io.Writer) error {
				defer cleaned.Store(true)
				<-workCtx.Done()
				return nvkrerr.Wrap(nvkrerr.CodeTimeout, workCtx.Err(), "worker canceled")
			}, nil, test.writer)
			assertCode(t, err, nvkrerr.CodePreflightFailed)
			if !cleaned.Load() || !screen.joined.Load() || !strings.Contains(err.Error(), "cannot display owned-cleanup wait notice") || strings.Contains(err.Error(), "private") {
				t.Fatal("notice failure was lost, leaked or skipped worker cleanup")
			}
		})
	}
}

type dashboardNoticeWriterFunc func([]byte) (int, error)

func (write dashboardNoticeWriterFunc) Write(data []byte) (int, error) { return write(data) }

func TestDashboardPresentationFailurePrecedesWorkerCancellation(t *testing.T) {
	for _, mode := range []string{"input", "closed", "renderer", "output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var outputFailed atomic.Bool
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), closed: mode == "closed", panicDraw: mode == "renderer"}
			view := newDashboard()
			view.recoveryWork = "/tmp/owned\x1bworkspace"
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			started, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var notice bytes.Buffer
			noticeSeen := make(chan struct{}, 1)
			noticeWriter := dashboardNoticeWriterFunc(func(data []byte) (int, error) {
				if !screen.joined.Load() {
					t.Error("cleanup notice preceded terminal restoration")
				}
				n, err := notice.Write(data)
				select {
				case noticeSeen <- struct{}{}:
				default:
				}
				return n, err
			})
			result := make(chan error, 1)
			go func() {
				result <- dashboardLoopWithCleanupNotice(ctx, cancel, screen, func() error {
					if outputFailed.Load() {
						return nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal output failed")
					}
					return nil
				}, writer, view, func(workCtx context.Context, _ io.Writer) error {
					close(started)
					<-workCtx.Done()
					close(cleaning)
					<-release
					return nvkrerr.Wrap(nvkrerr.CodeTimeout, workCtx.Err(), "worker canceled")
				}, nil, noticeWriter)
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not start")
			}
			switch mode {
			case "input":
				if err := screen.PostEvent(tcell.NewEventError(errors.New("private input error"))); err != nil {
					t.Fatal(err)
				}
			case "output":
				outputFailed.Store(true)
			}
			select {
			case <-cleaning:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not start cleanup")
			}
			if mode != "output" {
				select {
				case <-noticeSeen:
				case <-time.After(3 * time.Second):
					t.Fatal("restored terminal did not explain ongoing cleanup")
				}
			}
			select {
			case <-result:
				t.Fatal("presentation failure bypassed worker join")
			default:
			}
			close(release)
			select {
			case err := <-result:
				want := nvkrerr.CodePreflightFailed
				if mode == "renderer" {
					want = nvkrerr.CodeInternal
				}
				assertCode(t, err, want)
				if mode == "output" && notice.Len() != 0 || mode != "output" && (strings.Count(notice.String(), "waiting for the work owner to return") != 1 || strings.ContainsRune(notice.String(), '\x1b')) {
					t.Fatal("cleanup wait notice repeated, retried failed output or leaked terminal controls")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker cleanup did not join")
			}
		})
	}
}

func TestDashboardHandoverKeepsLateFailures(t *testing.T) {
	for _, mode := range []string{"writer", "terminal after replay", "terminal after completion", "same message distinct error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			first := nvkrerr.New(nvkrerr.CodeFetchFailed, "same public failure")
			late := nvkrerr.New(nvkrerr.CodePreflightFailed, "same public failure")
			var ownerErr error
			var out bytes.Buffer
			var output io.Writer = &out
			if mode == "writer" {
				output = failedProgressWriter{}
			}
			if mode == "same message distinct error" {
				ownerErr = first
			}
			writer := &checkedWriter{out: &out, cancel: cancel, started: time.Now(), results: [8]string{6: "Warning: result replay"}}
			calls := 0
			terminalFailure := func() error {
				calls++
				if mode == "same message distinct error" || mode == "terminal after replay" && calls >= 2 || mode == "terminal after completion" && calls >= 3 {
					return late
				}
				return nil
			}
			err := finishDashboardProgress(writer, output, "", ownerErr, terminalFailure)
			want := nvkrerr.CodePreflightFailed
			if ownerErr != nil {
				want = nvkrerr.CodeFetchFailed
			}
			assertCode(t, err, want)
			if mode == "writer" {
				if !errors.Is(err, writer.failure()) || ctx.Err() == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("late writer failure lost or leaked: %v", err)
				}
			} else if !errors.Is(err, late) || ownerErr != nil && !errors.Is(err, ownerErr) {
				t.Fatalf("distinct late terminal/owner failure lost: %v", err)
			}
			if mode != "terminal after completion" && strings.Contains(out.String(), "returned successfully") {
				t.Fatal("known replay failure printed successful completion")
			}
			if mode == "same message distinct error" && strings.Count(err.Error(), "same public failure") != 2 {
				t.Fatalf("text/code matching collapsed distinct errors: %v", err)
			}
		})
	}
}

func (s *testDashboardScreen) ChannelEvents(events chan<- tcell.Event, quit <-chan struct{}) {
	if s.onPumpExit != nil {
		defer s.onPumpExit()
	}
	if s.closed {
		close(events)
		return
	}
	s.SimulationScreen.ChannelEvents(events, quit)
}

func TestDashboardCancellationWaitsForWorkerCleanup(t *testing.T) {
	for _, test := range []struct {
		mode string
		code nvkrerr.Code
	}{
		{"context", nvkrerr.CodeTimeout},
		{"ctrl-c", nvkrerr.CodeTimeout},
		{"quit", nvkrerr.CodeTimeout},
		{"terminal-error", nvkrerr.CodePreflightFailed},
		{"closed-events", nvkrerr.CodePreflightFailed},
		{"output-error", nvkrerr.CodePreflightFailed},
		{"renderer-panic", nvkrerr.CodeInternal},
	} {
		t.Run(test.mode, func(t *testing.T) {
			mode := test.mode
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), closed: mode == "closed-events", panicDraw: mode == "renderer-panic"}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			started, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var failed atomic.Bool
			result := make(chan error, 1)
			go func() {
				result <- dashboardLoop(ctx, cancel, screen, func() error {
					if failed.Load() {
						return nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal output failed")
					}
					return nil
				}, writer, view, func(ctx context.Context, _ io.Writer) error {
					close(started)
					<-ctx.Done()
					close(cleaning)
					<-release
					return nil
				})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("workflow did not start")
			}
			switch mode {
			case "context":
				cancel()
			case "quit", "ctrl-c":
				key, value := tcell.KeyRune, 'q'
				if mode == "ctrl-c" {
					key, value = tcell.KeyCtrlC, 0
				}
				if err := screen.PostEvent(tcell.NewEventKey(key, value, tcell.ModNone)); err != nil {
					t.Fatal(err)
				}
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				deadline := time.After(3 * time.Second)
				for !view.snapshot().confirmCancel {
					select {
					case <-tick.C:
					case <-deadline:
						t.Fatal("quit confirmation did not open")
					}
				}
				if ctx.Err() != nil {
					t.Fatal("cancel key canceled work before confirmation")
				}
				select {
				case <-cleaning:
					t.Fatal("cancel key started cleanup before confirmation")
				default:
				}
				if err := screen.PostEvent(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone)); err != nil {
					t.Fatal(err)
				}
			case "terminal-error":
				if err := screen.PostEvent(tcell.NewEventError(errors.New("private terminal details"))); err != nil {
					t.Fatal(err)
				}
			case "output-error":
				failed.Store(true)
				if err := screen.PostEvent(tcell.NewEventResize(100, 40)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-cleaning:
			case <-time.After(3 * time.Second):
				t.Fatal("workflow was not canceled")
			}
			select {
			case <-result:
				t.Fatal("presenter returned before cleanup joined")
			default:
			}
			if (mode == "context" || mode == "ctrl-c" || mode == "quit") && screen.joined.Load() {
				t.Fatal("ordinary cancellation dropped its live cleanup display")
			}
			close(release)
			select {
			case err := <-result:
				assertCode(t, err, test.code)
				if !screen.joined.Load() || strings.Contains(err.Error(), "private") {
					t.Fatalf("lost cancellation, leaked private details or terminal unjoined: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("presenter or event pump did not join")
			}
		})
	}
}

func TestDashboardQuitConfirmationInput(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		key                    tcell.Key
		rune                   rune
		wantCancel, wantPrompt bool
	}{
		{"yes", tcell.KeyRune, 'y', true, false},
		{"uppercase yes", tcell.KeyRune, 'Y', true, false},
		{"no", tcell.KeyRune, 'n', false, false},
		{"uppercase no", tcell.KeyRune, 'N', false, false},
		{"escape dismisses", tcell.KeyEscape, 0, false, false},
		{"enter does not confirm", tcell.KeyEnter, 0, false, true},
		{"ctrl-c is not confirmation", tcell.KeyCtrlC, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			if d.input(tcell.NewEventKey(tcell.KeyRune, 'Q', tcell.ModNone)) || !d.snapshot().confirmCancel {
				t.Fatal("Q did not open confirmation without cancellation")
			}
			if got := d.input(tcell.NewEventKey(tc.key, tc.rune, tcell.ModNone)); got != tc.wantCancel || d.snapshot().confirmCancel != tc.wantPrompt {
				t.Fatalf("cancel=%v prompt=%v", got, d.snapshot().confirmCancel)
			}
		})
	}
}

func TestDashboardForceExitRequiresCancellationAndJoinsReturningCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	view := newDashboard()
	writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
	started, cleaning, release, forced := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var restoredBeforeForce atomic.Bool
	result := make(chan error, 1)
	go func() {
		result <- dashboardLoopWithForceExit(ctx, cancel, screen, func() error { return nil }, writer, view, func(ctx context.Context, _ io.Writer) error {
			close(started)
			<-ctx.Done()
			close(cleaning)
			<-release
			return nil
		}, func() {
			restoredBeforeForce.Store(screen.joined.Load())
			close(forced)
		})
	}()
	<-started
	post := func(key tcell.Key, value rune) {
		t.Helper()
		if err := screen.PostEvent(tcell.NewEventKey(key, value, tcell.ModNone)); err != nil {
			t.Fatal(err)
		}
	}
	waitPrompt := func(want bool) {
		t.Helper()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(3 * time.Second)
		for view.snapshot().confirmForce != want {
			select {
			case <-ticker.C:
			case <-deadline:
				t.Fatal("force confirmation did not change")
			}
		}
	}
	// These keys precede Ctrl-C in the same event queue. They must not request
	// force exit or start cleanup while the workflow is running.
	post(tcell.KeyRune, 'f')
	post(tcell.KeyRune, 'y')
	post(tcell.KeyCtrlC, 0)
	post(tcell.KeyRune, 'y')
	select {
	case <-cleaning:
	case <-time.After(3 * time.Second):
		t.Fatal("graceful cleanup did not start")
	}
	if view.snapshot().confirmForce {
		t.Fatal("running workflow opened force confirmation")
	}
	post(tcell.KeyRune, 'F')
	waitPrompt(true)
	post(tcell.KeyRune, 'N')
	waitPrompt(false)
	post(tcell.KeyRune, 'f')
	waitPrompt(true)
	post(tcell.KeyRune, 'Y')
	select {
	case <-forced:
		if !restoredBeforeForce.Load() {
			t.Fatal("force callback ran before terminal restoration")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed force callback did not run")
	}
	select {
	case <-result:
		t.Fatal("returning test callback bypassed worker join")
	default:
	}
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("forced cancellation fabricated successful completion")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker or event pump did not join")
	}
}

func TestDashboardForcedExitProcess(t *testing.T) {
	const marker = "NVKE_TEST_CONFIRMED_FORCE_EXIT"
	if os.Getenv(marker) == "1" {
		forceDashboardExit("/tmp/owned-demo")
		return
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDashboardForcedExitProcess$")
	command.Env = append(os.Environ(), marker+"=1")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 || !strings.Contains(string(output), "FORCED EXIT") || !strings.Contains(string(output), "Owned processes") || !strings.Contains(string(output), " restore --work '/tmp/owned-demo'") || !strings.Contains(string(output), "not tenant, VM or container deletion") || !strings.Contains(string(output), "cleanup is not confirmed") || strings.Contains(string(output), "PASS") {
		t.Fatalf("forced exit must warn and return 130, never PASS: %v %q", err, output)
	}
}

func TestDashboardInitializationAdmissionAndPlainFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), initFailure: true}
	err := dashboardLoop(ctx, cancel, screen, func() error { return nil }, nil, nil, func(context.Context, io.Writer) error {
		t.Fatal("failed terminal admission started workflow")
		return nil
	})
	assertCode(t, err, nvkrerr.CodePreflightFailed)
	for _, plain := range []bool{false, true} {
		var out bytes.Buffer
		if dashboardEligible(&out, plain, false) {
			t.Fatal("redirected writer selected terminal")
		}
		err := presentProgress(t.Context(), &out, nil, false, "", plain, func(_ context.Context, writer io.Writer) error {
			_, err := io.WriteString(writer, "PASS: owner result\n")
			return err
		})
		if err != nil || strings.ContainsRune(out.String(), '\x1b') || !strings.Contains(out.String(), "owner result") {
			t.Fatalf("plain output changed: %q %v", out.String(), err)
		}
	}
}

type shortTTY struct{ tcell.Tty }

func (shortTTY) Write([]byte) (int, error) { return 0, nil }

func TestCheckedTTYShortWriteCancelsWithoutPrivateDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tty := &checkedTTY{Tty: shortTTY{}, cancel: cancel}
	_, err := tty.Write([]byte("terminal bytes"))
	if !errors.Is(err, io.ErrShortWrite) || ctx.Err() == nil {
		t.Fatal("short terminal write did not cancel")
	}
	assertCode(t, tty.failure(), nvkrerr.CodePreflightFailed)
}

func TestDashboardWorkerPanicFinalFrameAndTeardown(t *testing.T) {
	for _, mode := range []string{"worker-panic", "final-frame", "teardown-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			var terminalFailed, cleaned atomic.Bool
			if mode == "teardown-error" {
				screen.onFini = func() { terminalFailed.Store(true) }
			}
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			err := dashboardLoop(ctx, cancel, screen, func() error {
				if terminalFailed.Load() {
					return nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal teardown failed")
				}
				return nil
			}, writer, view, func(_ context.Context, out io.Writer) error {
				defer cleaned.Store(true)
				if mode == "worker-panic" {
					panic("private worker details")
				}
				out.(nvkedemo.ProgressReporter).Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "artifacts"})
				return nil
			})
			if !cleaned.Load() || !screen.joined.Load() {
				t.Fatal("worker defers or terminal teardown did not finish")
			}
			if mode == "final-frame" {
				if err != nil || !strings.Contains(screen.lastFrame, "complete") || view.snapshot().phases[1].state != "complete" {
					t.Fatalf("final observation not rendered: %v\n%s", err, screen.lastFrame)
				}
			} else {
				code := nvkrerr.CodePreflightFailed
				if mode == "worker-panic" {
					code = nvkrerr.CodeInternal
				}
				assertCode(t, err, code)
				if strings.Contains(err.Error(), "private") {
					t.Fatal("private panic escaped presentation boundary")
				}
			}
		})
	}
}
