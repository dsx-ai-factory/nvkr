// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/term"
)

// Only the real interactive stdio is eligible. Never open /dev/tty behind a
// caller's redirected output or injected writer. Verbose builds remain plain.
func dashboardEligible(out io.Writer, plain, verbose bool) bool {
	return !plain && !verbose && out == os.Stdout && os.Getenv("TERM") != "" && os.Getenv("TERM") != "dumb" &&
		term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func presentProgress(ctx context.Context, out, raw io.Writer, verbose bool, logPath string, plain bool, work func(context.Context, io.Writer) error) error {
	if !dashboardEligible(out, plain, verbose) {
		return progressOutput(ctx, out, raw, verbose, logPath, work)
	}
	if err := ctx.Err(); err != nil {
		return nkderr.Wrap(nkderr.CodeTimeout, err, "demo canceled before terminal setup")
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tty, err := tcell.NewStdIoTty()
	if err != nil {
		return nkderr.New(nkderr.CodePreflightFailed, "cannot initialize terminal; retry with --plain")
	}
	monitored := &checkedTTY{Tty: tty, cancel: cancel}
	screen, err := tcell.NewTerminfoScreenFromTty(monitored)
	if err != nil {
		_ = monitored.Close()
		return nkderr.New(nkderr.CodePreflightFailed, "terminal capabilities unavailable; retry with --plain")
	}
	view := newDashboard()
	writer := &checkedWriter{out: view, observer: view, raw: raw, remaining: 8 << 20, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	if logPath != "" {
		writer.emit(fmt.Sprintf("Build log (private, retained; maximum 8 MiB): %q\n", logPath))
	}
	err = dashboardLoop(workCtx, cancel, screen, monitored.failure, writer, view, work)
	err = errors.Join(err, monitored.failure())
	// The terminal and worker have both joined before returning to scrolling
	// output. A failed renderer cannot skip workflow cleanup or fabricate PASS.
	writer.mu.Lock()
	writer.out = out
	writer.lineOutput()
	if writer.failedPhase != "" {
		writer.emit(fmt.Sprintf("Stopped at: %s\n", terminalText(writer.failedPhase)))
	}
	if logPath != "" {
		writer.emit(fmt.Sprintf("Retained build log: %q\n", logPath))
	}
	if err == nil {
		writer.emit("Demo command completed; its owned checks and cleanup returned successfully.\nUnqualified local diagnostic, not a signed Release.\n")
	}
	writer.mu.Unlock()
	return errors.Join(err, writer.failure(), monitored.failure())
}

// tcell intentionally ignores low-level writes and terminal teardown errors.
// Preserve the CLI's checked-output contract without implementing terminal I/O.
type checkedTTY struct {
	tcell.Tty
	mu     sync.Mutex
	err    error
	cancel context.CancelFunc
}

func (t *checkedTTY) record(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.err == nil {
		t.err = nkderr.New(nkderr.CodePreflightFailed, "terminal I/O failed; work canceled, owned cleanup still required")
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
	n, err := t.Tty.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	t.record(err)
	return n, err
}

func (t *checkedTTY) Stop() error {
	err := t.Tty.Stop()
	t.record(err)
	return err
}

func (t *checkedTTY) Close() error {
	err := t.Tty.Close()
	t.record(err)
	return err
}

// The event pump and workflow are the only caller-owned goroutines. Every exit
// joins both. Presentation callbacks update bounded snapshots, never wait for
// the UI queue. No tview Application/QueueUpdate acknowledgement can deadlock
// cancellation or cleanup when a terminal exits unexpectedly.
func dashboardLoop(ctx context.Context, cancel context.CancelFunc, screen tcell.Screen, outputFailure func() error, writer *checkedWriter, view *dashboard, work func(context.Context, io.Writer) error) (result error) {
	if err := screen.Init(); err != nil {
		// The pinned tcell implementation enters raw mode only in its final
		// successful Init stage. Earlier failures have no running screen;
		// Fini is unsafe before its internal stop channel is initialized.
		if tty, ok := screen.Tty(); ok {
			_ = tty.Close()
		}
		return nkderr.New(nkderr.CodePreflightFailed, "terminal initialization failed; no demo work started; retry with --plain")
	}
	defer func() {
		screen.Fini()
		result = errors.Join(result, outputFailure())
	}()
	if err := outputFailure(); err != nil {
		return err
	}
	events := make(chan tcell.Event, 8)
	quit, pumpDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(pumpDone)
		screen.ChannelEvents(events, quit)
	}()
	defer func() { close(quit); <-pumpDone }()
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recover() != nil {
				err = nkderr.New(nkderr.CodePreflightFailed, "demo worker panicked; owned deferred cleanup returned")
			}
			done <- err
		}()
		err = work(ctx, writer)
	}()
	joined := false
	defer func() {
		if recover() != nil {
			result = errors.Join(result, nkderr.New(nkderr.CodePreflightFailed, "terminal rendering failed; work canceled"))
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
			}
		}
		select {
		case result := <-done:
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
				canceled = nkderr.Wrap(nkderr.CodeTimeout, ctx.Err(), "demo canceled; owned cleanup returned")
			}
			return errors.Join(result, presentationErr, canceled)
		case <-ctxDone:
			canceling, ctxDone = true, nil
			cancel()
		case event, open := <-events:
			if !open {
				events = nil
				presentationErr = errors.Join(presentationErr, nkderr.New(nkderr.CodePreflightFailed, "terminal event stream ended; work canceled"))
				canceling, drawing = true, false
				cancel()
				continue
			}
			switch event := event.(type) {
			case *tcell.EventKey:
				if event.Key() == tcell.KeyCtrlC || event.Key() == tcell.KeyEscape || event.Rune() == 'q' {
					canceling = true
					cancel()
				} else {
					view.input(event)
				}
			case *tcell.EventResize:
				screen.Sync()
			case *tcell.EventError:
				presentationErr = errors.Join(presentationErr, nkderr.New(nkderr.CodePreflightFailed, "terminal input failed; work canceled"))
				canceling, drawing = true, false
				cancel()
			}
		case <-ticker.C:
		}
	}
}
