// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Only the real interactive stdio is eligible. Never open /dev/tty behind a
// caller's redirected output or injected writer. Verbose builds remain plain.
func dashboardEligible(out io.Writer, plain, verbose bool) bool {
	return dashboardTerminalEligible(out == os.Stdout, plain, verbose, os.Getenv("TERM"),
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())))
}

func dashboardTerminalEligible(stdio, plain, verbose bool, terminal string, inputTTY, outputTTY bool) bool {
	return stdio && !plain && !verbose && terminal != "" && terminal != "dumb" && inputTTY && outputTTY
}

type recoveryWorkKey struct{}

// A configured path is recovery guidance only, never proof of resource ownership
// or a completed admission. The recovery owner must authenticate its journal.
func progressRecoveryContext(ctx context.Context, work string) (context.Context, error) {
	if strings.TrimSpace(work) == "" {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "recovery workspace requires an explicit configured path")
	}
	absolute, err := filepath.Abs(work)
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "locate configured recovery workspace")
	}
	return context.WithValue(ctx, recoveryWorkKey{}, absolute), nil
}

func progressRecoveryWork(ctx context.Context) string {
	work, _ := ctx.Value(recoveryWorkKey{}).(string)
	return work
}

func dashboardRecoveryGuidance(work, executable string) string {
	message := "Owned processes, VMs, containers and network changes may remain; cleanup is not confirmed."
	if work != "" {
		// Follow the existing factory shellQuote pattern. Terminal escaping is
		// separate: quoted shell syntax must not turn controls into live bytes.
		message += "\nConfigured recovery workspace (escaped): " + terminalText(work)
		if executable != "" && utf8.ValidString(work) && strings.IndexFunc(work, unicode.IsControl) < 0 && utf8.ValidString(executable) && strings.IndexFunc(executable, unicode.IsControl) < 0 {
			quoted := "'" + strings.ReplaceAll(work, "'", `'"'"'`) + "'"
			binary := "'" + strings.ReplaceAll(executable, "'", `'"'"'`) + "'"
			message += "\nIf its original network-state.json exists, after inspecting and stopping surviving owned producers: " + binary + " restore --work " + quoted
		} else {
			message += "\nIf its original network-state.json exists, after stopping owned producers pass your original configured workspace to nvke-demo restore --work; the escaped locator is not a shell argument."
		}
	}
	return message + "\nRestore handles recorded network controls only, not tenant, VM or container deletion. See docs/nvkr-to-nvke-demo.md#recover-network-controls-after-a-crash."
}

func presentProgress(ctx context.Context, out, raw io.Writer, verbose bool, logPath string, plain bool, work func(context.Context, io.Writer) error, auth ...func(context.Context) error) error {
	if !dashboardEligible(out, plain, verbose) {
		return progressOutput(ctx, out, raw, verbose, logPath, work, auth...)
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "demo canceled before terminal setup")
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tty, err := tcell.NewStdIoTty()
	if err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "cannot initialize terminal; retry with --plain")
	}
	inputFD := int(os.Stdin.Fd())
	inputFlags, err := unix.FcntlInt(uintptr(inputFD), unix.F_GETFL, 0)
	if err != nil {
		_ = tty.Close()
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "cannot inspect terminal file flags; no demo work started; retry with --plain")
	}
	monitored := &checkedTTY{Tty: tty, cancel: cancel, inputFD: inputFD, inputFlags: inputFlags, restoreInputFlags: true}
	screen, err := tcell.NewTerminfoScreenFromTty(monitored)
	if err != nil {
		_ = monitored.Close()
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal capabilities unavailable; retry with --plain")
	}
	view := newDashboard()
	view.buildLogPath = logPath
	view.recoveryWork = progressRecoveryWork(ctx)
	writer := &checkedWriter{out: view, observer: view, raw: raw, remaining: 8 << 20, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	if len(auth) > 0 {
		writer.sudoAuth = auth[0]
	}
	// The admitted locator is durable presentation state, not an escaped,
	// evictable activity line. Plain output retains its startup notice.
	err = dashboardLoopWithForceExit(workCtx, cancel, screen, monitored.failure, writer, view, work, func() { forceDashboardExit(view.recoveryWork) })
	// The terminal and worker have both joined before returning to scrolling
	// output. A failed renderer cannot skip workflow cleanup or fabricate PASS.
	return finishDashboardProgress(writer, out, logPath, err, monitored.failure)
}

func finishDashboardProgress(writer *checkedWriter, out io.Writer, logPath string, err error, terminalFailure func() error) error {
	if terminalFailure != nil {
		if failure := terminalFailure(); failure != nil && !errors.Is(err, failure) {
			err = errors.Join(err, failure)
		}
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.lineOutput()
	writer.out = out
	writer.replayResults(err == nil)
	if writer.failedPhase != "" {
		writer.emit(fmt.Sprintf("Stopped at: %s\n", terminalText(writer.failedPhase)))
	}
	if logPath != "" {
		writer.emit(fmt.Sprintf("Retained build log: %q\n", logPath))
	}
	if failure := writer.err; failure != nil && !errors.Is(err, failure) {
		err = errors.Join(err, failure)
	}
	if terminalFailure != nil {
		if failure := terminalFailure(); failure != nil && !errors.Is(err, failure) {
			err = errors.Join(err, failure)
		}
	}
	if err == nil {
		writer.emit("Demo command returned successfully.\nUnqualified local diagnostic, not a signed Release.\n")
	}
	// Teardown and scrollback writes can fail after the owner has returned.
	// Retain distinct failures, but do not repeat a terminal sentinel already
	// joined by the loop or its final teardown. Never deduplicate by text/code.
	if failure := writer.err; failure != nil && !errors.Is(err, failure) {
		err = errors.Join(err, failure)
	}
	if terminalFailure != nil {
		if failure := terminalFailure(); failure != nil && !errors.Is(err, failure) {
			err = errors.Join(err, failure)
		}
	}
	return err
}

// This callback is installed only at the real interactive CLI boundary. A
// confirmed forced exit abandons the cleanup wait, never claims successful
// cleanup and never returns normally with an unjoined worker.
func forceDashboardExit(work string) {
	executable, _ := os.Executable()
	_, _ = fmt.Fprintln(os.Stderr, "FORCED EXIT: stopped waiting for cleanup. "+dashboardRecoveryGuidance(work, executable))
	os.Exit(130)
}

// tcell intentionally ignores low-level writes and terminal teardown errors.
// Preserve the CLI's checked-output contract without implementing terminal I/O.
type checkedTTY struct {
	tcell.Tty
	mu                sync.Mutex
	err               error
	cancel            context.CancelFunc
	inputFD           int
	inputFlags        int
	restoreInputFlags bool
}

func (t *checkedTTY) record(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.err == nil {
		t.err = nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal I/O failed; work canceled, owned cleanup still required")
		t.cancel()
	}
	t.mu.Unlock()
}

func (t *checkedTTY) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *checkedTTY) Write(data []byte) (int, error) {
	// disengage joins its readers before emitting teardown writes. Restore
	// shared stdio flags for those writes too, not only the later Stop call.
	if err := t.restoreFlags(); err != nil {
		t.record(err)
		return 0, err
	}
	n, err := t.Tty.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	t.record(err)
	return n, err
}

func (t *checkedTTY) restoreFlags() error {
	if !t.restoreInputFlags {
		return nil
	}
	_, err := unix.FcntlInt(uintptr(t.inputFD), unix.F_SETFL, t.inputFlags)
	return err
}

func (t *checkedTTY) Stop() error {
	err := t.Tty.Stop()
	// The pinned stdio Tty's Drain sets O_NONBLOCK, but Stop restores only
	// termios. stdin/stdout may share that open description. After readers join,
	// restore admitted flags before sudo or a resumed screen reuses stdio.
	err = errors.Join(err, t.restoreFlags())
	t.record(err)
	return err
}

func (t *checkedTTY) Close() error {
	err := errors.Join(t.Tty.Close(), t.restoreFlags())
	t.record(err)
	return err
}

// The event pump and workflow are the only caller-owned goroutines. Every normal
// return joins both; only confirmed process termination bypasses the join.
// Presentation callbacks update bounded snapshots, never wait for
// the UI queue. No tview Application/QueueUpdate acknowledgement can deadlock
// cancellation or cleanup when a terminal exits unexpectedly.
func dashboardLoop(ctx context.Context, cancel context.CancelFunc, screen tcell.Screen, outputFailure func() error, writer *checkedWriter, view *dashboard, work func(context.Context, io.Writer) error) (result error) {
	return dashboardLoopWithForceExit(ctx, cancel, screen, outputFailure, writer, view, work, nil)
}

func dashboardLoopWithForceExit(ctx context.Context, cancel context.CancelFunc, screen tcell.Screen, outputFailure func() error, writer *checkedWriter, view *dashboard, work func(context.Context, io.Writer) error, forceExit func()) (result error) {
	return dashboardLoopWithCleanupNotice(ctx, cancel, screen, outputFailure, writer, view, work, forceExit, os.Stderr)
}

func dashboardLoopWithCleanupNotice(ctx context.Context, cancel context.CancelFunc, screen tcell.Screen, outputFailure func() error, writer *checkedWriter, view *dashboard, work func(context.Context, io.Writer) error, forceExit func(), notice io.Writer) (result error) {
	if err := screen.Init(); err != nil {
		// The pinned tcell implementation enters raw mode only in its final
		// successful Init stage. Earlier failures have no running screen;
		// Fini is unsafe before its internal stop channel is initialized.
		if tty, ok := screen.Tty(); ok {
			_ = tty.Close()
		}
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal initialization failed; no demo work started; retry with --plain")
	}
	terminalRestored := false
	defer func() {
		if !terminalRestored {
			screen.Fini()
		}
		if failure := outputFailure(); failure != nil && !errors.Is(result, failure) {
			result = errors.Join(result, failure)
		}
	}()
	if err := outputFailure(); err != nil {
		return err
	}
	view.mu.Lock()
	view.forceAvailable = forceExit != nil
	view.mu.Unlock()
	var events chan tcell.Event
	var quit, pumpDone chan struct{}
	startPump := func() {
		events = make(chan tcell.Event, 8)
		quit, pumpDone = make(chan struct{}), make(chan struct{})
		go func(events chan tcell.Event, quit, pumpDone chan struct{}) {
			defer close(pumpDone)
			screen.ChannelEvents(events, quit)
		}(events, quit, pumpDone)
	}
	stopPump := func() {
		if quit != nil {
			close(quit)
			<-pumpDone
			quit, events = nil, nil
		}
	}
	// Failed presentation releases terminal input before the owner's cleanup
	// joins. Ordinary cancellation keeps the screen until that join completes.
	restoreTerminal := func() {
		stopPump()
		if !terminalRestored {
			screen.Fini()
			terminalRestored = true
		}
	}
	noticeWritten := false
	cleanupNotice := func() error {
		// Never retry output after a known terminal-write failure. Input failure
		// and renderer failure can still leave scrolling stderr usable after Fini.
		if noticeWritten || outputFailure() != nil {
			return nil
		}
		noticeWritten = true
		message := "Terminal display failed; waiting for the work owner to return. Retained resources require explicit cleanup."
		if view.recoveryWork != "" {
			message += " Recovery workspace (configured, escaped): " + terminalText(view.recoveryWork)
		}
		if n, err := fmt.Fprintln(notice, message); err != nil || n != len(message)+1 {
			return nvkrerr.New(nvkrerr.CodePreflightFailed, "cannot display owned-cleanup wait notice")
		}
		return nil
	}
	startPump()
	defer stopPump()
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			done <- err
		}()
		err = runProgressWork(ctx, writer, work)
	}()
	joined := false
	defer func() {
		if value := recover(); value != nil {
			// A broken renderer cannot keep the terminal raw for the entire
			// owner's cleanup budget. Stop readers before restoring the screen,
			// then retain the normal mandatory worker join below.
			cancel()
			restoreTerminal()
			result = errors.Join(result, recoveredPanic(ctx, "terminal renderer", value), cleanupNotice())
		}
		cancel()
		if !joined {
			result = errors.Join(result, <-done)
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	ctxDone := ctx.Done()
	var presentationErr error
	canceling, drawing := false, true
	for {
		if drawing {
			view.draw(screen, time.Now(), canceling)
			screen.Show()
			if err := outputFailure(); err != nil {
				presentationErr = errors.Join(presentationErr, err)
				canceling, drawing = true, false
				cancel()
				restoreTerminal()
			}
		}
		select {
		case workErr := <-done:
			// Preserve the joined owner's result even if the final draw panics.
			result = workErr
			joined = true
			// Completion events can arrive after the last refresh. Render the
			// owner's final observation before restoring the terminal.
			if drawing {
				view.draw(screen, time.Now(), canceling)
				screen.Show()
				presentationErr = errors.Join(presentationErr, outputFailure())
			}
			var canceled error
			if ctx.Err() != nil {
				canceled = nvkrerr.Wrap(nvkrerr.CodeTimeout, ctx.Err(), "demo work returned after cancellation; inspect retained/cleanup instructions")
			}
			// A presentation failure caused cancellation; the resulting worker
			// timeout is secondary and must not turn terminal failure into retryable work.
			return errors.Join(presentationErr, result, canceled)
		case <-ctxDone:
			canceling, ctxDone = true, nil
			cancel()
		case event, open := <-events:
			if !open {
				events = nil
				presentationErr = errors.Join(presentationErr, nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal event stream ended; work canceled"))
				canceling, drawing = true, false
				cancel()
				restoreTerminal()
				presentationErr = errors.Join(presentationErr, cleanupNotice())
				continue
			}
			switch event := event.(type) {
			case *tcell.EventKey:
				if canceling && forceExit != nil && view.forceInput(event) {
					// Restore the terminal before the process-exit callback. A
					// returning test callback still follows the normal join path.
					drawing = false
					restoreTerminal()
					exit := forceExit
					forceExit = nil
					exit()
				} else if !canceling && view.input(event) {
					canceling = true
					cancel()
				}
			case *tcell.EventResize:
				screen.Sync()
			case *tcell.EventError:
				presentationErr = errors.Join(presentationErr, nvkrerr.New(nvkrerr.CodePreflightFailed, "terminal input failed; work canceled"))
				canceling, drawing = true, false
				cancel()
				restoreTerminal()
				presentationErr = errors.Join(presentationErr, cleanupNotice())
			}
		case <-ticker.C:
		}
	}
}
