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
	"syscall"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sync/errgroup"
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

// Authenticate before terminal presentation, then maintain the existing sudo
// cache only for this workflow. No credential is read by Go and no policy is
// changed. Every later check is non-interactive, including cleanup checks.
func withTerminalSudo(ctx context.Context, out, diagnostics io.Writer, work func(context.Context, func(context.Context) error) error) error {
	authenticate := terminalSudoAuthenticator(out, diagnostics)
	if authenticate == nil {
		return work(ctx, nil)
	}
	if _, err := fmt.Fprintln(diagnostics, "This demo keeps your existing sudo session valid while it runs. Authenticate now; the dashboard will not ask for passwords mid-run."); err != nil {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "show networking authentication notice")
	}
	return withSudoSession(ctx, authenticate, refreshTerminalSudo, 30*time.Second, work)
}

func refreshTerminalSudo(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "sudo", "-n", "-v")
	cmd.Env = nkedemo.NativeEnvironment()
	if err := nkedemo.RunAuthenticationCommand(bounded, cmd); err != nil {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "sudo session is no longer usable; no password prompt was opened; retained artifacts remain available; restore any remaining network controls from their original workspace")
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
	session, stop := context.WithCancel(ctx)
	group, active := errgroup.WithContext(session)
	defer func() {
		if recover() != nil {
			result = errors.Join(result, nkderr.New(nkderr.CodePreflightFailed, "sudo session workflow panicked; owned refresh stopped and joined"))
		}
		stop()
		result = errors.Join(result, group.Wait())
	}()
	group.Go(func() error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-active.Done():
				return nil
			case <-ticker.C:
				if err := refresh(active); err != nil {
					if active.Err() != nil && !errors.Is(err, nkedemo.ErrUnjoined) {
						return nil
					}
					return err
				}
			}
		}
	})
	return work(active, refresh)
}

func authenticateTerminalSudo(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// This read-only validation may succeed without a prompt. The actual root
	// command still decides its own policy; validation does not authorize it.
	probe := exec.CommandContext(ctx, "sudo", "-n", "-v")
	probe.Env = nkedemo.NativeEnvironment()
	if err := nkedemo.RunAuthenticationCommand(ctx, probe); err == nil {
		return nil
	} else if errors.Is(err, nkedemo.ErrUnjoined) {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "administrator authentication probe could not join; no prompt started")
	}
	if err := ctx.Err(); err != nil {
		return nkderr.Wrap(nkderr.CodeTimeout, err, "administrator authentication canceled")
	}
	if _, err := fmt.Fprintln(os.Stderr, "Administrator authentication is needed before starting the demo. At sudo's prompt, type your password (typing is hidden) and press Enter. Authentication has a two-minute deadline; Ctrl+C cancels."); err != nil {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "show administrator authentication prompt")
	}
	cmd := exec.CommandContext(ctx, "sudo", "-v")
	cmd.Env = nkedemo.NativeEnvironment()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runForegroundAuthentication(ctx, cmd, int(os.Stdin.Fd()))
}

// Go's existing Foreground process option and x/sys ioctl helpers own terminal
// job control. The child keeps its private process group for bounded stop/join.
// Terminal restoration blocks SIGTTOU on its OS thread, without changing the
// process-wide signal disposition or interfering with another subscriber.
func runForegroundAuthentication(ctx context.Context, cmd *exec.Cmd, fd int) (result error) {
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return nkderr.New(nkderr.CodePreflightFailed, "administrator authentication requires the foreground terminal")
	}
	defer func() {
		if err := restoreTerminalForeground(fd, foreground); err != nil {
			result = errors.Join(result, errTerminalNotRestored, nkderr.Wrap(nkderr.CodePreflightFailed, err, "restore terminal after administrator authentication"))
		}
	}()
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: fd}
	if err := nkedemo.RunAuthenticationCommand(ctx, cmd); err != nil {
		return terminalAuthenticationFailure(ctx, err)
	}
	return nil
}

func terminalAuthenticationFailure(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nkderr.Wrap(nkderr.CodeTimeout, err, "sudo password authentication deadline expired; retry from an interactive terminal and respond to sudo's prompt; no privileged command was retried")
	case errors.Is(ctx.Err(), context.Canceled):
		return nkderr.Wrap(nkderr.CodeTimeout, err, "sudo password authentication canceled; no privileged command was retried")
	default:
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "sudo password authentication failed; no privileged command was retried")
	}
}

var errTerminalNotRestored = errors.New("authentication terminal foreground was not restored")

func (w *checkedWriter) SudoAuthentication() func(context.Context) error { return w.sudoAuth }
