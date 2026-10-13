// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package runpresenter owns descriptive terminal presentation, never execution authority.
package runpresenter

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Options supplies a simulation screen for tests or uses the real admitted stdio.
type Options struct {
	Screen  tcell.Screen
	Live    bool
	NoColor bool
	// Diagnostics is an optional bounded private capture, never a terminal writer
	// or safe progress stream. Its caller may replay bytes after Run returns.
	Diagnostics *rundiagnostics.Buffer
}

// Result keeps the owning result separate from checked presentation failures.
type Result struct{ WorkErr, PresentationErr error }

// Run joins both owned goroutines on every normal return. A nil work closure
// opens a read-only viewer; q closes it without touching the observed owner.
func Run(ctx context.Context, options Options, source interface{ Snapshot() progress.Snapshot }, work func(context.Context) error) (result Result) {
	if ctx == nil || source == nil || options.Live != (work != nil) {
		result.PresentationErr = nvkrerr.New(nvkrerr.CodeUsage, "terminal session requires a snapshot source and matching execution mode")
		return result
	}
	if err := ctx.Err(); err != nil {
		result.WorkErr = canceled(err)
		return result
	}
	if options.Screen == nil {
		if !Eligible(os.Stdout) {
			result.PresentationErr = nvkrerr.New(nvkrerr.CodeUsage, "interactive output requires terminal stdin and stdout")
			return result
		}
		// A controlling-terminal hangup must cancel and join this session's
		// owner instead of taking Go's default SIGHUP process-exit path. The
		// handler remains installed until the later session cleanup has joined.
		hupCtx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
		defer stop()
		ctx = hupCtx
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	screen := options.Screen
	var monitored *checkedTTY
	if screen == nil {
		flags, err := unix.FcntlInt(os.Stdin.Fd(), unix.F_GETFL, 0)
		if err != nil {
			result.PresentationErr = terminalError("terminal flags unavailable; no work started")
			return result
		}
		initialState, err := term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			result.PresentationErr = terminalError("terminal state unavailable; no work started")
			return result
		}
		tty, err := tcell.NewStdIoTty()
		if err != nil {
			result.PresentationErr = terminalError("terminal setup failed; no work started")
			return result
		}
		monitored = &checkedTTY{Tty: tty, cancel: cancel, fd: int(os.Stdin.Fd()), flags: flags, restore: true, initialState: initialState}
		screen, err = tcell.NewTerminfoScreenFromTty(monitored)
		if err != nil {
			_ = monitored.Close()
			result.PresentationErr = terminalError("terminal capabilities unavailable; no work started")
			return result
		}
	}
	if err := screen.Init(); err != nil {
		// tcell Fini is unsafe before a successful Init initializes its stop channel.
		if tty, ok := screen.Tty(); ok {
			_ = tty.Close()
		}
		result.PresentationErr = terminalError("terminal initialization failed; no work started")
		return result
	}
	var quit, pumpDone chan struct{}
	var done chan error
	joined, restored := false, false
	restore := func() {
		if quit != nil {
			close(quit)
			<-pumpDone
			quit = nil
		}
		if !restored {
			restored = true
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						capturePanic(options.Diagnostics, "terminal restoration", recovered)
						result.PresentationErr = errors.Join(result.PresentationErr, nvkrerr.New(nvkrerr.CodeInternal, "terminal restoration failed"))
					}
				}()
				screen.Fini()
			}()
		}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			capturePanic(options.Diagnostics, "terminal renderer", recovered)
			result.PresentationErr = errors.Join(result.PresentationErr, nvkrerr.New(nvkrerr.CodeInternal, "terminal renderer failed"))
			cancel()
		}
		// Restore input before waiting for a failed renderer's canceled owner.
		restore()
		cancel()
		if done != nil && !joined {
			result.WorkErr = <-done
		}
		if monitored != nil && monitored.failure() != nil && !errors.Is(result.PresentationErr, monitored.failure()) {
			result.PresentationErr = errors.Join(result.PresentationErr, monitored.failure())
		}
	}()
	if monitored != nil && monitored.failure() != nil {
		result.PresentationErr = monitored.failure()
		return result
	}
	events := make(chan tcell.Event, 8)
	pumpFailure := make(chan error, 1)
	quit, pumpDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(pumpDone)
		defer func() {
			if recovered := recover(); recovered != nil {
				capturePanic(options.Diagnostics, "terminal event reader", recovered)
				pumpFailure <- terminalError("terminal event reader failed")
			}
		}()
		screen.ChannelEvents(events, quit)
	}()
	if work != nil {
		done = make(chan error, 1)
		go func() {
			var err error
			defer func() {
				if recovered := recover(); recovered != nil {
					capturePanic(options.Diagnostics, "Run worker", recovered)
					err = nvkrerr.New(nvkrerr.CodeInternal, "Run worker failed")
				}
				done <- err
			}()
			err = work(workCtx)
		}()
	}
	view := NewView(options.Live, options.NoColor || os.Getenv("NO_COLOR") != "")
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	drawing, canceling := true, false
	ctxDone := ctx.Done()
	for {
		if drawing {
			view.Draw(screen, source.Snapshot(), time.Now(), canceling)
			screen.Show()
			if monitored != nil && monitored.failure() != nil {
				result.PresentationErr = monitored.failure()
				cancel()
				restore()
				drawing, canceling = false, true
				if done == nil {
					return result
				}
			}
		}
		select {
		case failure := <-pumpFailure:
			result.PresentationErr = failure
			cancel()
			restore()
			drawing, canceling = false, true
			events = nil
			if done == nil {
				return result
			}
		case err := <-done:
			joined = true
			view.dismissConfirmation()
			result.WorkErr = err
			if drawing {
				// The owner has joined. Cancellation is no longer a wait state;
				// show its final snapshot without replacing its returned error.
				view.Draw(screen, source.Snapshot(), time.Now(), false)
				screen.Show()
			}
			return result
		case <-ctxDone:
			view.dismissConfirmation()
			cancel()
			canceling = true
			ctxDone = nil
			if done == nil {
				result.WorkErr = canceled(ctx.Err())
				return result
			}
		case event, open := <-events:
			if !open {
				result.PresentationErr = terminalError("terminal event stream ended")
				cancel()
				restore()
				drawing, canceling = false, true
				events = nil
				if done == nil {
					return result
				}
				continue
			}
			switch event := event.(type) {
			case *tcell.EventError:
				result.PresentationErr = terminalError("terminal input failed")
				cancel()
				restore()
				drawing, canceling = false, true
				events = nil
				if done == nil {
					return result
				}
			case *tcell.EventResize:
				screen.Sync()
			case *tcell.EventKey:
				switch view.Input(event) {
				case Close:
					return result
				case Cancel:
					cancel()
					canceling = true
					if done == nil {
						result.WorkErr = canceled(context.Canceled)
						return result
					}
				}
			}
		case <-tick.C:
		}
	}
}

// capturePanic retains a bounded current-goroutine stack behind the private sink
// boundary. Only builtin strings are copied as values; arbitrary panic objects
// contribute their type without executing Error or String methods.
func capturePanic(sink *rundiagnostics.Buffer, location string, value any) {
	if sink == nil {
		return
	}
	var raw [rundiagnostics.MaxBytes]byte
	n := copy(raw[:], "Recovered "+location+" panic")
	if text, ok := value.(string); ok {
		n += copy(raw[n:], ": ")
		n += copy(raw[n:], text[:min(len(text), 4096)])
		if len(text) > 4096 {
			n += copy(raw[n:], " [value truncated]")
		}
	} else {
		n += copy(raw[n:], " (type: ")
		name := "<nil>"
		if kind := reflect.TypeOf(value); kind != nil {
			name = kind.String()
		}
		n += copy(raw[n:], name[:min(len(name), 256)])
		n += copy(raw[n:], ")")
	}
	n += copy(raw[n:], "\nCurrent goroutine stack (bounded):\n")
	n += runtime.Stack(raw[n:], false)
	_, _ = sink.Write(raw[:n])
}

// canceled maps presentation cancellation to the public timeout code while
// retaining its cause for callers that distinguish cancellation from deadlines.
func canceled(err error) error {
	return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "Run presentation canceled")
}

// terminalError reports a static terminal failure without exposing raw I/O text.
func terminalError(message string) error {
	return nvkrerr.New(nvkrerr.CodeUnavailable, "%s", message)
}

// checkedTTY retains errors deliberately ignored by the pinned terminal stack.
// Stdio Drain changes shared file flags; restoring termios alone is insufficient.
type checkedTTY struct {
	tcell.Tty
	mu           sync.Mutex
	err          error
	cancel       context.CancelFunc
	fd, flags    int
	restore      bool
	initialState *term.State
}

// record latches the first terminal failure and cancels the session owner.
// The sanitized failure remains available after terminal restoration and join.
func (t *checkedTTY) record(err error) {
	if err != nil {
		t.mu.Lock()
		if t.err == nil {
			t.err = terminalError("terminal I/O failed")
			t.cancel()
		}
		t.mu.Unlock()
	}
}

// failure returns the latched error under the same lock used by terminal writers.
func (t *checkedTTY) failure() error { t.mu.Lock(); defer t.mu.Unlock(); return t.err }

// restoreFlags restores the admitted stdin file flags after tcell's drain work.
// Injected TTYs can disable restoration when no real descriptor was admitted.
func (t *checkedTTY) restoreFlags() error {
	if !t.restore {
		return nil
	}
	_, err := unix.FcntlInt(uintptr(t.fd), unix.F_SETFL, t.flags)
	return err
}

// Write restores shared descriptor flags before writing and records short writes.
// It leaves termios alone while the session still owns raw terminal input.
func (t *checkedTTY) Write(data []byte) (int, error) {
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

// Stop retains both the terminal stop failure and descriptor restoration failure.
func (t *checkedTTY) Stop() error {
	err := errors.Join(t.Tty.Stop(), t.restoreFlags())
	t.record(err)
	return err
}

// Close restores descriptor flags and the original cooked state after tcell has
// joined its readers, retaining cleanup failures for the session result. Darwin
// additionally settles pending-input bookkeeping without consuming user input.
func (t *checkedTTY) Close() error {
	err := errors.Join(t.Tty.Close(), t.restoreFlags())
	// tcell joins its readers before Close. Restore the original cooked state
	// explicitly here, after Stop's raw-to-cooked transition can set Darwin
	// PENDIN. Never restore termios in Write while terminal input is still raw.
	if t.initialState != nil {
		err = errors.Join(err, term.Restore(t.fd, t.initialState))
		if runtime.GOOS == "darwin" {
			// XNU tty.c preserves PENDIN through termios setters. A bounded
			// readiness query invokes ttyselect/ttnread/ttypend to settle the
			// raw-to-canonical transition without reading or flushing input.
			// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/tty.c
			_, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}, 0)
			err = errors.Join(err, pollErr)
			// Preserve even an originally requested pending-input bit after
			// the kernel's nonconsuming readiness bookkeeping has settled.
			err = errors.Join(err, term.Restore(t.fd, t.initialState))
		}
	}
	t.record(err)
	return err
}
