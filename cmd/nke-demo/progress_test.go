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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

func TestProgressModeAdmission(t *testing.T) {
	for _, test := range []struct {
		name, mode, term string
		tty, color, tui  bool
		code             nkderr.Code
	}{
		{"auto-terminal", "auto", "xterm-256color", true, true, true, ""},
		{"pipe", "auto", "xterm", false, true, false, ""},
		{"dumb", "auto", "dumb", true, true, false, ""},
		{"unset", "auto", "", true, true, false, ""},
		{"no-color", "auto", "xterm", true, false, false, ""},
		{"plain", "plain", "xterm", true, true, false, ""},
		{"forced-pipe", "tui", "xterm", false, true, false, nkderr.CodeEngineCapability},
		{"forced-dumb", "tui", "dumb", true, true, false, nkderr.CodeEngineCapability},
		{"forced-no-color", "tui", "xterm", true, false, false, nkderr.CodeEngineCapability},
		{"invalid", "unknown", "xterm", true, true, false, nkderr.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectProgress(test.mode, test.tty, test.color, test.term)
			assertCode(t, err, test.code)
			if got != test.tui {
				t.Fatal("incorrect presentation selection")
			}
		})
	}
}

func TestProgressConsoleSanitizesAndBounds(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{"csi", "before\x1b[2J\x1b[Hafter", "beforeafter"},
		{"osc-clipboard", "a\x1b]52;c;c2VjcmV0\aZ", "aZ"},
		{"osc-link", "\x1b]8;;https://private.example\x1b\\visible\x1b]8;;\x1b\\", "visible"},
		{"controls", "a\x00\x7f\u0085\u202e\u2066b\u2028c\tD", "abc D"},
		{"utf8", "a\xffb", "ab"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := safeConsole(test.input, consoleBytes); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
	b := newProgressBuffer(func() {})
	for _, part := range []string{"safe\x1b]52;", "c;c2VjcmV0", "\a done\n"} {
		if _, err := io.WriteString(b, part); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.snapshot().lines; len(got) != 1 || got[0] != "safe done" {
		t.Fatalf("split escape escaped sanitizer: %q", got)
	}
	if _, err := io.WriteString(b, strings.Repeat("x", 100_000)+"\n"); err != nil {
		t.Fatal(err)
	}
	for range consoleLines + 40 {
		if _, err := io.WriteString(b, "line\n"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := b.snapshot()
	if len(snapshot.lines) != consoleLines || snapshot.omitted != 42 {
		t.Fatalf("unbounded or inaccurate tail: %d/%d", len(snapshot.lines), snapshot.omitted)
	}
	if len(b.partial) > consoleBytes || cap(b.partial) > 2*consoleBytes {
		t.Fatal("partial buffer grew without bound")
	}
	b.close()
	b.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "os"})
	if n, err := io.WriteString(b, "late"); n != 4 || err != nil {
		t.Fatal("late write blocked or failed")
	}
}

func TestProgressEventsBoundedAndImmutable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := newProgressBuffer(cancel)
	for range eventLimit + 1 {
		b.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: "compile"})
	}
	if ctx.Err() == nil || len(b.events) != eventLimit {
		t.Fatal("event overflow was silently lost or unbounded")
	}
	assertCode(t, b.failure(), nkderr.CodePreflightFailed)
	first := b.snapshot()
	first.events[0].Name = "modified"
	if len(b.snapshot().events) != 0 {
		t.Fatal("snapshot did not drain events")
	}
	m := newProgressModel(t.Context(), newProgressBuffer(func() {}), "e2e", make(chan struct{}), io.Discard)
	initial := m
	updated, _ := m.Update(progressSnapshot{events: []nkedemo.ProgressEvent{{Kind: nkedemo.LaneStarted, Name: "os"}, {Kind: nkedemo.LaneFinished, Name: "os"}, {Kind: nkedemo.LaneStarted, Name: "os"}, {Kind: nkedemo.PhaseStarted, Name: "new-phase"}, {Kind: nkedemo.PhaseFailed, Name: "old-phase"}}, lines: []string{"fresh"}, now: time.Now()})
	m = updated.(progressModel)
	if initial.lanes[0].status != "waiting" || initial.lines != nil || m.lanes[0].status != "finished" || m.phaseStatus != "started" {
		t.Fatal("mutable view state or regressed event ordering")
	}
	before := m
	updated, _ = m.Update(progressSnapshot{lines: []string{"replacement"}, now: time.Now()})
	if before.lines[0] != "fresh" || updated.(progressModel).lines[0] != "replacement" {
		t.Fatal("console slices share mutable state")
	}
}

func TestProgressViewResponsiveAndDoesNotInventAcceptance(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 32}, {30, 12}} {
		t.Run(strings.Repeat("x", size[0]), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := newProgressModel(ctx, newProgressBuffer(cancel), "e2e", make(chan struct{}), io.Discard)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = updated.(progressModel)
			m.lines = []string{strings.Repeat("long console ", 80)}
			for _, line := range strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("row wider than terminal: %q", line)
				}
			}
			if rows := strings.Count(m.View(), "\n"); rows > size[1] {
				t.Fatalf("frame exceeds terminal: %d", rows)
			}
			m.done = true
			if strings.Contains(m.View(), "COMPLETED") {
				t.Fatal("EOF fabricated completion")
			}
			m.deployment = "finished"
			if !strings.Contains(m.View(), "COMPLETED") {
				t.Fatal("actual completion omitted")
			}
			m.failed = true
			if strings.Contains(m.View(), "COMPLETED") {
				t.Fatal("failure rendered success")
			}
		})
	}
}

type failedProgressWriter struct{ short bool }

func (w failedProgressWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) / 2, nil
	}
	return 0, errors.New("private writer details must not be exposed")
}
func TestPlainProgressPropagatesCancellationAndOutputFailures(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			joined := false
			err := runProgress(t.Context(), "plain", failedProgressWriter{short: short}, "build", func(ctx context.Context, writer io.Writer) error {
				_, _ = io.WriteString(writer, "public output")
				if ctx.Err() == nil {
					t.Fatal("failed output did not cancel owned work")
				}
				joined = true
				return nil
			})
			assertCode(t, err, nkderr.CodePreflightFailed)
			if !joined || strings.Contains(err.Error(), "private writer") {
				t.Fatal("work unjoined or private error exposed")
			}
		})
	}
	var out bytes.Buffer
	err := runProgress(t.Context(), "auto", &out, "build", func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, "plain\n")
		return err
	})
	if err != nil || out.String() != "plain\n" {
		t.Fatal("pipe fallback changed public output")
	}
}

func TestTerminalProgressCancellationJoinsBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	joined := false
	err := runTerminalProgress(ctx, cancel, &out, "e2e", func(ctx context.Context, writer io.Writer) error {
		writer.(nkedemo.ProgressReporter).Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "os"})
		cancel()
		<-ctx.Done()
		// Model refresh must remain alive during actual cleanup after cancellation.
		time.Sleep(180 * time.Millisecond)
		joined = true
		return nkderr.New(nkderr.CodeTimeout, "work canceled and joined")
	})
	assertCode(t, err, nkderr.CodeTimeout)
	if !joined || !strings.Contains(ansi.Strip(out.String()), "STOPPING / JOINING") {
		t.Fatal("renderer exited before owned cleanup")
	}
}

func TestTerminalOutputFailureStartsNoWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := false
	err := runTerminalProgress(ctx, cancel, failedProgressWriter{}, "build", func(context.Context, io.Writer) error { called = true; return nil })
	assertCode(t, err, nkderr.CodePreflightFailed)
	if called {
		t.Fatal("terminal startup failure performed runtime work")
	}
}

func TestProgressBufferConcurrentLateReports(t *testing.T) {
	b := newProgressBuffer(func() {})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 50 {
				b.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: "public"})
				_, _ = io.WriteString(b, "line\n")
				_ = b.snapshot()
			}
		})
	}
	b.finish(nil)
	b.close()
	workers.Wait()
}

func TestNonblockingOutputIsBounded(t *testing.T) {
	for _, test := range []struct {
		name     string
		canceled bool
		fault    error
	}{
		{"backpressure", false, unix.EAGAIN}, {"interrupted", false, unix.EINTR}, {"canceled", true, unix.EAGAIN},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			started := time.Now()
			n, err := writeNonblocking(ctx, 25*time.Millisecond, []byte("frame"), func([]byte) (int, error) { return 0, test.fault })
			if err == nil || n != 0 || time.Since(started) > time.Second {
				t.Fatal("nonblocking writer did not stop within budget")
			}
		})
	}
	var received []byte
	n, err := writeNonblocking(t.Context(), time.Second, []byte("frame"), func(data []byte) (int, error) { received = append(received, data[0]); return 1, nil })
	if err != nil || n != 5 || string(received) != "frame" {
		t.Fatal("partial syscall writes lost bytes")
	}
}

type sizingProbe struct {
	progressModel
	sizes chan tea.WindowSizeMsg
}

func (probe sizingProbe) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		select {
		case probe.sizes <- size:
		default:
		}
	}
	model, command := probe.progressModel.Update(message)
	probe.progressModel = model.(progressModel)
	return probe, command
}

// A real PTY establishes that BubbleTea sees the full term.File interface and
// reads initial and resized dimensions, rather than testing synthetic messages
// alone. This is a presentation fixture, not an NKE build or acceptance run.
func TestProgressActualPTY(t *testing.T) {
	if os.Getenv("NKE_DEMO_TEST_PTY_CHILD") != "1" {
		if runtime.GOOS != "darwin" {
			t.Skip("native macOS script PTY fixture; Linux syscall contracts run separately")
		}
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "/usr/bin/script", "-q", filepath.Join(t.TempDir(), "terminal.log"), binary, "-test.run=^TestProgressActualPTY$", "-test.count=1")
		command.Env = append(os.Environ(), "NKE_DEMO_TEST_PTY_CHILD=1", "TERM=xterm-256color")
		command.WaitDelay = time.Second
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "real PTY sizing verified") {
			t.Fatalf("real PTY fixture failed: %v %s", err, output)
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	before, err := unix.FcntlInt(os.Stdout.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := openProgressTerminal(ctx, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = terminal.Close() }()
	after, err := unix.FcntlInt(os.Stdout.Fd(), unix.F_GETFL, 0)
	if err != nil || after != before {
		t.Fatal("caller stdout flags changed")
	}
	if err := unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 71, Row: 23}); err != nil {
		t.Fatal(err)
	}
	writer := &terminalProgressWriter{checkedWriter: &checkedWriter{out: terminal, cancel: cancel}, file: terminal}
	var _ term.File = writer
	b := newProgressBuffer(cancel)
	ready := make(chan struct{})
	sizes := make(chan tea.WindowSizeMsg, 4)
	m := sizingProbe{progressModel: newProgressModel(ctx, b, "build", ready, writer), sizes: sizes}
	program := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(writer), tea.WithInput(nil), tea.WithoutSignalHandler(), tea.WithoutBracketedPaste())
	type result struct {
		model tea.Model
		err   error
	}
	done := make(chan result, 1)
	go func() { model, err := program.Run(); done <- result{model, err} }()
	select {
	case <-ready:
	case <-ctx.Done():
		program.Kill()
		<-done
		t.Fatal("terminal did not initialize")
	}
	select {
	case initial := <-sizes:
		if initial.Width != 71 || initial.Height != 23 {
			program.Kill()
			<-done
			t.Fatalf("real initial size not observed: %+v", initial)
		}
	case <-ctx.Done():
		program.Kill()
		<-done
		t.Fatal("initial size query stalled")
	}
	if err := unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 55, Row: 19}); err != nil {
		program.Kill()
		<-done
		t.Fatal(err)
	}
	if err := unix.Kill(os.Getpid(), unix.SIGWINCH); err != nil {
		program.Kill()
		<-done
		t.Fatal(err)
	}
	select {
	case resized := <-sizes:
		if resized.Width != 55 || resized.Height != 19 {
			program.Kill()
			<-done
			t.Fatalf("real resize not observed: %+v", resized)
		}
	case <-ctx.Done():
		program.Kill()
		<-done
		t.Fatal("resize query stalled")
	}
	b.finish(nil)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	final := got.model.(sizingProbe).progressModel
	if final.width != 55 || final.height != 19 {
		t.Fatalf("real resize not observed: %dx%d", final.width, final.height)
	}
	fmtWriter := os.Stdout
	if _, err := io.WriteString(fmtWriter, "real PTY sizing verified\n"); err != nil {
		t.Fatal(err)
	}
}
