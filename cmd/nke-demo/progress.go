// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

const (
	consoleLines = 120
	consoleBytes = 4096
	eventLimit   = 256
)

func validateProgress(mode string) error {
	if mode != "auto" && mode != "plain" && mode != "tui" {
		return nkderr.New(nkderr.CodeUsage, "--progress must be auto, plain or tui")
	}
	return nil
}

func selectProgress(mode string, terminal, color bool, terminalName string) (bool, error) {
	if err := validateProgress(mode); err != nil {
		return false, err
	}
	usable := terminal && color && terminalName != "" && terminalName != "dumb"
	if mode == "tui" && !usable {
		return false, nkderr.New(nkderr.CodeEngineCapability, "TUI requires a terminal, a non-dumb TERM and no NO_COLOR; use --progress plain")
	}
	return mode != "plain" && usable, nil
}

// runProgress owns presentation only. Work remains synchronous, including its
// bounded child stop/join and cleanup, even if the terminal renderer fails.
func runProgress(ctx context.Context, mode string, out io.Writer, operation string, work func(context.Context, io.Writer) error) error {
	file, terminal := out.(interface{ Fd() uintptr })
	if terminal {
		terminal = term.IsTerminal(file.Fd())
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	tui, err := selectProgress(mode, terminal, !noColor, os.Getenv("TERM"))
	if err != nil {
		return err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !tui {
		writer := &checkedWriter{out: out, cancel: cancel}
		result := work(workCtx, writer)
		return errors.Join(result, writer.failure())
	}
	terminalFile, err := openProgressTerminal(workCtx, out)
	if err != nil {
		if mode == "auto" {
			writer := &checkedWriter{out: out, cancel: cancel}
			result := work(workCtx, writer)
			return errors.Join(result, writer.failure())
		}
		return err
	}
	defer func() { _ = terminalFile.Close() }()
	return runTerminalProgress(workCtx, cancel, terminalFile, operation, work)
}

// The existing platform tty utility owns tty-name lookup. Reopening that exact
// device avoids changing the caller's file-description flags. macOS PTYs do
// not support os.File write deadlines, so use pinned x/sys nonblocking I/O.
func openProgressTerminal(ctx context.Context, out io.Writer) (*progressTerminal, error) {
	file, ok := out.(*os.File)
	if !ok {
		return nil, nkderr.New(nkderr.CodeEngineCapability, "TUI requires a native terminal file")
	}
	budget, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(budget, "/usr/bin/tty")
	command.Stdin = file
	command.Env = []string{"LANG=C"}
	command.WaitDelay = time.Second
	data, err := command.Output()
	if err != nil {
		return nil, nkderr.New(nkderr.CodeEngineCapability, "cannot resolve the selected terminal; use --progress plain")
	}
	path := strings.TrimSpace(string(data))
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, "/dev/") {
		return nil, nkderr.New(nkderr.CodeEngineCapability, "unsupported terminal device")
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nkderr.New(nkderr.CodeEngineCapability, "cannot open an independent terminal; use --progress plain")
	}
	var original, opened unix.Stat_t
	flags, flagsErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if unix.Fstat(int(file.Fd()), &original) != nil || unix.Fstat(fd, &opened) != nil || original.Rdev != opened.Rdev || original.Dev != opened.Dev || original.Ino != opened.Ino || flagsErr != nil || flags&unix.O_NONBLOCK == 0 || !term.IsTerminal(uintptr(fd)) {
		_ = unix.Close(fd)
		return nil, nkderr.New(nkderr.CodeEngineCapability, "independent terminal admission failed; use --progress plain")
	}
	return &progressTerminal{fd: fd, ctx: context.WithoutCancel(ctx), budget: 2 * time.Second}, nil
}

type progressTerminal struct {
	fd       int
	ctx      context.Context
	budget   time.Duration
	once     sync.Once
	closeErr error
}

func (t *progressTerminal) Fd() uintptr            { return uintptr(t.fd) }
func (*progressTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (t *progressTerminal) Close() error {
	t.once.Do(func() { t.closeErr = unix.Close(t.fd) })
	return t.closeErr
}
func (t *progressTerminal) Write(data []byte) (int, error) {
	return writeNonblocking(t.ctx, t.budget, data, func(data []byte) (int, error) { return unix.Write(t.fd, data) })
}

// This is bounded nonblocking syscall I/O, not terminal escape interpretation.
// It owns no goroutine; cancellation/deadline and partial writes stay explicit.
func writeNonblocking(ctx context.Context, budget time.Duration, data []byte, write func([]byte) (int, error)) (int, error) {
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	total := 0
	for total < len(data) {
		if err := bounded.Err(); err != nil {
			return total, err
		}
		n, err := write(data[total:])
		if n > 0 {
			total += n
		}
		if err == nil && n > 0 {
			continue
		}
		if err == nil {
			return total, io.ErrShortWrite
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return total, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return total, bounded.Err()
		case <-timer.C:
		}
	}
	return total, nil
}

type terminalProgressWriter struct {
	*checkedWriter
	file term.File
}

func (w *terminalProgressWriter) Fd() uintptr            { return w.file.Fd() }
func (*terminalProgressWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (*terminalProgressWriter) Close() error             { return nil } // ownership remains with runProgress
var _ term.File = (*terminalProgressWriter)(nil)

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
		// Do not copy output bytes or arbitrary writer errors into diagnostics.
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

type progressSnapshot struct {
	events  []nkedemo.ProgressEvent
	lines   []string
	omitted uint64
	done    bool
	failed  bool
	now     time.Time
}

// No public event or console write blocks on a renderer. Terminal lane events
// cannot be silently dropped; event overflow cancels work. Console history is
// intentionally a bounded, lossy tail, with its omitted-line count displayed.
type progressBuffer struct {
	mu        sync.Mutex
	events    []nkedemo.ProgressEvent
	lines     []string
	partial   []byte
	truncated bool
	omitted   uint64
	done      bool
	failed    bool
	closed    bool
	err       error
	changed   chan struct{}
	cancel    context.CancelFunc
}

func newProgressBuffer(cancel context.CancelFunc) *progressBuffer {
	return &progressBuffer{changed: make(chan struct{}, 1), cancel: cancel}
}
func (b *progressBuffer) signal() {
	select {
	case b.changed <- struct{}{}:
	default:
	}
}
func (b *progressBuffer) Report(event nkedemo.ProgressEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.done {
		return
	}
	switch event.Kind {
	case nkedemo.LaneStarted, nkedemo.LaneFinished, nkedemo.LaneFailed, nkedemo.PhaseStarted, nkedemo.PhaseFinished, nkedemo.PhaseFailed, nkedemo.DeploymentStarted, nkedemo.DeploymentFinished, nkedemo.DeploymentFailed:
	default:
		return
	}
	if len(b.events) == eventLimit {
		if b.err == nil {
			b.err = nkderr.New(nkderr.CodePreflightFailed, "progress event queue exhausted; work canceled")
			b.cancel()
		}
	} else {
		event.Name = safeConsole(event.Name, 160)
		event.Status = "" // Kind is the typed operation boundary, not caller text.
		b.events = append(b.events, event)
	}
	b.signal()
}
func (b *progressBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.done {
		return len(data), nil
	}
	for _, value := range data {
		if value == '\n' || value == '\r' {
			if len(b.partial) > 0 || b.truncated || value == '\n' {
				b.flushLine()
			}
		} else if len(b.partial) < consoleBytes {
			b.partial = append(b.partial, value)
		} else {
			b.truncated = true
		}
	}
	b.signal()
	return len(data), nil
}
func (b *progressBuffer) flushLine() {
	line := safeConsole(string(b.partial), consoleBytes)
	if b.truncated {
		line += " [line truncated]"
	}
	if len(b.lines) == consoleLines {
		copy(b.lines, b.lines[1:])
		b.lines = b.lines[:consoleLines-1]
		b.omitted++
	}
	b.lines = append(b.lines, line)
	b.partial = b.partial[:0]
	b.truncated = false
}
func (b *progressBuffer) finish(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.partial) > 0 || b.truncated {
		b.flushLine()
	}
	b.done, b.failed = true, err != nil || b.err != nil
	b.signal()
}
func (b *progressBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.signal()
}
func (b *progressBuffer) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}
func (b *progressBuffer) snapshot() progressSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := progressSnapshot{events: append([]nkedemo.ProgressEvent(nil), b.events...), lines: append([]string(nil), b.lines...), omitted: b.omitted, done: b.done, failed: b.failed, now: time.Now()}
	b.events = b.events[:0]
	return snapshot
}

// The upstream ANSI state machine strips CSI/OSC/DCS sequences. The second
// pass removes standard Unicode controls and bidi formatting, not a homemade
// terminal protocol parser. Raw input is bounded before this function runs.
func safeConsole(value string, limit int) string {
	if len(value) > consoleBytes {
		value = value[:consoleBytes]
	}
	value = strings.ToValidUTF8(ansi.Strip(value), "")
	var out strings.Builder
	for _, r := range value {
		if r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			continue
		}
		if out.Len()+len(string(r)) > limit {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

type rendererResult struct{ err error }
type progressReady struct{}

func runTerminalProgress(ctx context.Context, cancelWork context.CancelFunc, out io.Writer, operation string, work func(context.Context, io.Writer) error) error {
	renderCtx, stopRender := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRender()
	buffer := newProgressBuffer(cancelWork)
	defer buffer.close()
	writer := &checkedWriter{out: out, cancel: func() { cancelWork(); stopRender() }}
	var renderOut io.Writer = writer
	if file, ok := out.(term.File); ok {
		renderOut = &terminalProgressWriter{checkedWriter: writer, file: file}
	}
	ready := make(chan struct{})
	model := newProgressModel(renderCtx, buffer, operation, ready, renderOut)
	model.workCtx = ctx
	program := tea.NewProgram(model, tea.WithContext(renderCtx), tea.WithOutput(renderOut), tea.WithInput(nil), tea.WithoutSignalHandler(), tea.WithoutBracketedPaste(), tea.WithFPS(10))
	result := make(chan rendererResult, 1)
	var workFinished atomic.Bool
	go func() {
		_, err := program.Run()
		if !workFinished.Load() {
			cancelWork()
		}
		result <- rendererResult{err: err}
	}()
	select {
	case <-ready:
	case <-result:
		if writer.failure() != nil {
			return writer.failure()
		}
		return nkderr.New(nkderr.CodePreflightFailed, "progress renderer failed before work")
	case <-ctx.Done():
		stopRender()
		<-result
		if writer.failure() != nil {
			return writer.failure()
		}
		return nkderr.Wrap(nkderr.CodeTimeout, ctx.Err(), "local demo canceled before work")
	}
	// This synchronous call owns all runtime work. Cancellation of the renderer
	// does not bypass the library's bounded cleanup or child-process joins.
	var workErr error
	if ctx.Err() == nil && writer.failure() == nil {
		workErr = work(ctx, buffer)
	} else {
		workErr = nkderr.New(nkderr.CodeTimeout, "local demo canceled before work")
	}
	workFinished.Store(true)
	buffer.finish(workErr)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var rendered rendererResult
	select {
	case rendered = <-result:
	case <-timer.C:
		// Framework cancellation stops and joins its command/renderer handlers.
		// Keep ownership through the join; never leave runtime work behind.
		stopRender()
		program.Kill()
		rendered = <-result
		return errors.Join(workErr, nkderr.New(nkderr.CodeTimeout, "progress renderer shutdown exceeded its budget"))
	}
	var renderErr error
	if rendered.err != nil && writer.failure() == nil {
		renderErr = nkderr.New(nkderr.CodePreflightFailed, "progress renderer stopped unexpectedly")
	}
	return errors.Join(workErr, buffer.failure(), writer.failure(), renderErr)
}

type laneState struct{ name, status string }
type progressModel struct {
	ctx                            context.Context
	workCtx                        context.Context
	buffer                         *progressBuffer
	ready                          chan struct{}
	readyOnce                      *sync.Once
	styles                         *lipgloss.Renderer
	operation                      string
	lanes                          [6]laneState
	phase, phaseStatus, deployment string
	lines                          []string
	omitted                        uint64
	width, height                  int
	started, now                   time.Time
	done, failed                   bool
}

func newProgressModel(ctx context.Context, buffer *progressBuffer, operation string, ready chan struct{}, out io.Writer) progressModel {
	now := time.Now()
	return progressModel{ctx: ctx, workCtx: ctx, buffer: buffer, ready: ready, readyOnce: &sync.Once{}, styles: lipgloss.NewRenderer(out), operation: operation, width: 100, height: 30, started: now, now: now,
		lanes: [6]laneState{{"os", "waiting"}, {"components", "waiting"}, {"site", "waiting"}, {"tools", "waiting"}, {"profiles", "waiting"}, {"simulator", "waiting"}}}
}
func (m progressModel) Init() tea.Cmd { return func() tea.Msg { return progressReady{} } }
func (m progressModel) await() tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-m.buffer.changed:
		case <-timer.C:
		case <-m.ctx.Done():
			return tea.QuitMsg{}
		}
		return m.buffer.snapshot()
	}
}
func (m progressModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case progressReady:
		m.readyOnce.Do(func() { close(m.ready) })
		return m, m.await()
	case tea.WindowSizeMsg:
		m.width, m.height = max(20, msg.Width), max(12, msg.Height)
	case progressSnapshot:
		for _, event := range msg.events {
			m.apply(event)
		}
		m.lines = append([]string(nil), msg.lines...)
		m.omitted, m.now, m.done, m.failed = msg.omitted, msg.now, msg.done, msg.failed
		if m.done {
			return m, tea.Quit
		}
		return m, m.await()
	}
	return m, nil
}
func (m *progressModel) apply(event nkedemo.ProgressEvent) {
	switch event.Kind {
	case nkedemo.LaneStarted, nkedemo.LaneFinished, nkedemo.LaneFailed:
		for index := range m.lanes {
			lane := &m.lanes[index]
			if lane.name != event.Name || lane.status == "finished" || lane.status == "failed" {
				continue
			}
			switch event.Kind {
			case nkedemo.LaneStarted:
				lane.status = "started"
			case nkedemo.LaneFinished:
				lane.status = "finished"
			case nkedemo.LaneFailed:
				lane.status = "failed"
			}
		}
	case nkedemo.PhaseStarted:
		m.phase, m.phaseStatus = safeConsole(event.Name, 160), "started"
	case nkedemo.PhaseFinished, nkedemo.PhaseFailed:
		if m.phase == event.Name {
			m.phaseStatus = "finished"
			if event.Kind == nkedemo.PhaseFailed {
				m.phaseStatus = "failed"
			}
		}
	case nkedemo.DeploymentStarted:
		m.deployment = "started"
	case nkedemo.DeploymentFinished:
		m.deployment = "finished"
	case nkedemo.DeploymentFailed:
		m.deployment = "failed"
	}
}

func (m progressModel) paint(text, color string) string {
	return m.styles.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}
func (m progressModel) View() string {
	width := max(20, m.width)
	clip := func(value string, columns int) string { return ansi.Truncate(value, max(1, columns), "...") }
	elapsed := m.now.Sub(m.started).Truncate(time.Second)
	status, color := "EXECUTING", "#47B8FF"
	if m.workCtx.Err() != nil && !m.done {
		status, color = "STOPPING / JOINING", "#EEAF34"
	}
	if m.done {
		status, color = "FINISHED", "#8191A5"
		if m.failed {
			status, color = "FAILED / CLEANUP RETURNED", "#D94764"
		} else if m.deployment == "finished" || m.operation == "build" && m.completed() == 6 {
			status, color = "COMPLETED", "#76B900"
		}
	}
	header := m.paint("NKE  /  LOCAL ASSEMBLY", "#F0F4F8") + "\n" + m.paint(fmt.Sprintf("%s   %s   elapsed %s", strings.ToUpper(m.operation), status, elapsed), color)
	var rail []string
	rail = append(rail, m.paint("ARTIFACT LANES", "#8191A5"))
	for _, lane := range m.lanes {
		mark, laneColor := "[ ]", "#EEAF34"
		switch lane.status {
		case "started":
			mark, laneColor = "[>]", "#47B8FF"
		case "finished":
			mark, laneColor = "[+]", "#76B900"
		case "failed":
			mark, laneColor = "[!]", "#D94764"
		}
		state := lane.status
		if m.operation == "run" || m.operation == "restore" {
			state = "not rebuilt"
			mark, laneColor = "[-]", "#8191A5"
		}
		rail = append(rail, m.paint(fmt.Sprintf("%s %-11s %s", mark, lane.name, state), laneColor))
	}
	if m.operation == "build" || m.operation == "e2e" {
		rail = append(rail, m.paint(fmt.Sprintf("%d/6 observed lane completions", m.completed()), "#8191A5"))
	}
	rail = append(rail, "", m.paint("CURRENT PHASE", "#8191A5"))
	phase := m.phase
	if phase == "" {
		phase = "awaiting operation events"
	}
	phaseWidth, consoleWidth := width, width
	if width >= 100 {
		phaseWidth, consoleWidth = 34, width-38
	}
	rail = append(rail, clip(m.paint(phase, "#F0F4F8"), phaseWidth), clip(m.paint(m.phaseStatus, "#8191A5"), phaseWidth))
	consoleHeight := max(2, m.height-7)
	if width < 100 {
		consoleHeight = max(2, m.height-len(rail)-7)
	}
	console := []string{m.paint("PUBLIC BUILD CONSOLE", "#8191A5")}
	if m.omitted > 0 {
		console = append(console, m.paint(fmt.Sprintf("%d earlier lines omitted", m.omitted), "#8191A5"))
	}
	for _, line := range m.lines[max(0, len(m.lines)-consoleHeight):] {
		console = append(console, clip(line, consoleWidth))
	}
	if len(m.lines) == 0 {
		console = append(console, m.paint("No public console output yet.", "#8191A5"))
	}
	var body string
	if width >= 100 {
		left := m.styles.NewStyle().Width(34).Render(strings.Join(rail, "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", strings.Join(console, "\n"))
	} else {
		body = strings.Join(rail, "\n") + "\n\n" + strings.Join(console, "\n")
	}
	footer := "Local diagnostic / unqualified. Ctrl+C cancels; cleanup stays owned."
	rows := strings.Split(header+"\n\n"+body, "\n")
	if len(rows) > m.height-2 {
		rows = rows[:max(1, m.height-2)]
	}
	rows = append(rows, "", m.paint(footer, "#8191A5"))
	for index := range rows {
		rows[index] = clip(rows[index], width)
	}
	return strings.Join(rows, "\n") + "\n"
}
func (m progressModel) completed() int {
	count := 0
	for _, lane := range m.lanes {
		if lane.status == "finished" {
			count++
		}
	}
	return count
}
