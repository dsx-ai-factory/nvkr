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
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Only the CLI's actual terminal can opt into password authentication. Injected
// or redirected streams keep the unattended lane; we never open /dev/tty or
// route a password through a Go reader, diagnostic sink or recording log.
func terminalSudoAuthenticator(out, diagnostics io.Writer) func(context.Context) error {
	if runtime.GOOS != "linux" || out != os.Stdout || diagnostics != os.Stderr || !term.IsTerminal(int(os.Stdin.Fd())) ||
		!term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return nil
	}
	return authenticateTerminalSudo
}

// Prefer the approved unattended wrapper. Otherwise authenticate before terminal
// presentation and maintain the existing cache only for this workflow. No
// credential is read by Go and no policy is changed. Later checks never prompt.
func withTerminalSudo(ctx context.Context, out, diagnostics io.Writer, work func(context.Context, func(context.Context) error) error) error {
	authenticate := terminalSudoAuthenticator(out, diagnostics)
	if authenticate == nil {
		return work(ctx, nil)
	}
	return selectSudoSession(ctx, diagnostics, probeUnattendedSudo, authenticate, refreshTerminalSudo, work)
}

func probeUnattendedSudo(ctx context.Context) error {
	return runUnattendedSudoProbe(ctx, nvkedemo.RunAuthenticationCommand)
}

func runUnattendedSudoProbe(ctx context.Context, run func(context.Context, *exec.Cmd) error) error {
	bounded, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	// Match the network owner's exact unattended wrapper. This ignores cached
	// authentication; approval of sudo -v alone is neither needed nor sufficient.
	cmd := exec.CommandContext(bounded, "sudo", "-n", "-k", "timeout", "--signal=TERM", "--kill-after=5", "25", "true")
	cmd.Env = nvkedemo.NativeEnvironment()
	return run(bounded, cmd)
}

func selectSudoSession(ctx context.Context, diagnostics io.Writer, probe, authenticate, refresh func(context.Context) error, work func(context.Context, func(context.Context) error) error) error {
	if err := probe(ctx); err == nil {
		return work(ctx, nil)
	} else if errors.Is(err, nvkedemo.ErrUnjoined) {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "unattended sudo probe could not join; no authentication or workflow started")
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "networking authentication canceled before workflow admission")
	}
	if _, err := fmt.Fprintln(diagnostics, "Preparing administrator authentication before the demo. Later checks are non-interactive; the dashboard will not ask for passwords mid-run."); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "show networking authentication notice")
	}
	return withSudoSession(ctx, authenticate, refresh, 30*time.Second, work)
}

func refreshTerminalSudo(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "sudo", "-n", "-v")
	cmd.Env = nvkedemo.NativeEnvironment()
	if err := nvkedemo.RunAuthenticationCommand(bounded, cmd); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "non-interactive sudo session check failed; no password prompt or networking command was started by this check")
	}
	return nil
}

func withSudoSession(ctx context.Context, authenticate, refresh func(context.Context) error, interval time.Duration, work func(context.Context, func(context.Context) error) error) (result error) {
	if err := authenticate(ctx); err != nil {
		return err
	}
	// Prove that the isolated non-interactive child can use this host's cache
	// before starting work. A successful prompt is not sufficient admission.
	if err := refresh(ctx); err != nil {
		return err
	}
	// Cleanup runs inside work with its own bounded context after work is
	// canceled. Keep refreshing until that cleanup returns, then stop and join.
	session, stop := context.WithCancel(context.WithoutCancel(ctx))
	active, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	var group errgroup.Group
	serial := semaphore.NewWeighted(1)
	var mu sync.Mutex
	var latest, unjoined error
	check := func(checkCtx context.Context) error {
		if err := serial.Acquire(checkCtx, 1); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "non-interactive sudo check canceled while waiting for the previous check")
		}
		defer serial.Release(1)
		mu.Lock()
		fatal := unjoined
		mu.Unlock()
		if fatal != nil {
			return fatal
		}
		err := refresh(checkCtx)
		mu.Lock()
		if errors.Is(err, nvkedemo.ErrUnjoined) {
			unjoined, latest = err, err
			cancelWork()
		} else if checkCtx.Err() == nil || !errors.Is(err, checkCtx.Err()) {
			// A joined transient failure is retried, not permission to cancel a
			// long build. Success clears it; actual root commands remain gated.
			latest = err
		}
		mu.Unlock()
		return err
	}
	defer func() {
		if value := recover(); value != nil {
			result = errors.Join(result, recoveredPanic(ctx, "sudo session workflow", value))
		}
		stop()
		result = errors.Join(result, group.Wait())
		mu.Lock()
		// A joined keepalive failure gated no privileged command. Every such
		// command checks again; it cannot reverse an already accepted result.
		if result != nil || unjoined != nil {
			result = errors.Join(result, latest)
		}
		mu.Unlock()
	}()
	group.Go(func() error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-session.Done():
				return nil
			case <-ticker.C:
				if err := check(session); errors.Is(err, nvkedemo.ErrUnjoined) {
					return nil
				}
			}
		}
	})
	return work(active, check)
}

func authenticateTerminalSudo(ctx context.Context) error {
	return authenticateSudo(ctx, nvkedemo.RunAuthenticationCommand, runForegroundAuthentication, os.Stdin, os.Stdout, os.Stderr, int(os.Stdin.Fd()))
}

func authenticateSudo(ctx context.Context, run func(context.Context, *exec.Cmd) error, foreground func(context.Context, *exec.Cmd, int) error, input io.Reader, output, diagnostics io.Writer, fd int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// This read-only validation may succeed without a prompt. The actual root
	// command still decides its own policy; validation does not authorize it.
	probe := exec.CommandContext(ctx, "sudo", "-n", "-v")
	probe.Env = nvkedemo.NativeEnvironment()
	if err := run(ctx, probe); err == nil {
		return nil
	} else if errors.Is(err, nvkedemo.ErrUnjoined) {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "administrator authentication probe could not join; no prompt started")
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "administrator authentication canceled")
	}
	if _, err := fmt.Fprintln(diagnostics, "Administrator authentication is needed before starting the demo. At sudo's prompt, type your password (typing is hidden) and press Enter. Authentication has a two-minute deadline; Ctrl+C cancels."); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "show administrator authentication prompt")
	}
	cmd := exec.CommandContext(ctx, "sudo", "-v")
	cmd.Env = nvkedemo.NativeEnvironment()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, diagnostics
	return foreground(ctx, cmd, fd)
}

// Go's existing Foreground process option and x/sys ioctl helpers own terminal
// job control. The child keeps its private process group for bounded stop/join.
// Terminal restoration blocks SIGTTOU on its OS thread, without changing the
// process-wide signal disposition or interfering with another subscriber.
func runForegroundAuthentication(ctx context.Context, cmd *exec.Cmd, fd int) (result error) {
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "administrator authentication requires the foreground terminal")
	}
	state, err := term.GetState(fd)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "save terminal before administrator authentication")
	}
	defer func() {
		if err := restoreAuthenticationTerminal(fd, foreground, state); err != nil {
			result = errors.Join(result, errTerminalNotRestored, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "restore terminal after administrator authentication"))
		}
	}()
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: fd}
	if err := nvkedemo.RunAuthenticationCommand(ctx, cmd); err != nil {
		return terminalAuthenticationFailure(ctx, err)
	}
	return nil
}

func terminalAuthenticationFailure(ctx context.Context, err error) error {
	var exit *exec.ExitError
	interrupted := errors.As(err, &exit)
	if interrupted {
		status, ok := exit.Sys().(syscall.WaitStatus)
		interrupted = ok && status.Signaled() && status.Signal() == syscall.SIGINT
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "sudo password authentication deadline expired; retry from an interactive terminal and respond to sudo's prompt; no privileged command was retried")
	case errors.Is(ctx.Err(), context.Canceled) || interrupted:
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, errors.Join(err, context.Canceled), "sudo password authentication canceled; no privileged command was retried")
	default:
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "sudo password authentication failed; no privileged command was retried")
	}
}

var errTerminalNotRestored = errors.New("authentication terminal foreground was not restored")

func (w *checkedWriter) SudoAuthentication() func(context.Context) error { return w.sudoAuth }
