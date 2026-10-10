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
	"github.com/gdamore/tcell/v2"
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
	if _, err := fmt.Fprintln(os.Stderr, "Administrator authentication is needed for demo networking. Enter your password at sudo's prompt; Ctrl+C cancels. Cleanup may ask again after a long build."); err != nil {
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
		code := nkderr.CodePreflightFailed
		if ctx.Err() != nil {
			code = nkderr.CodeTimeout
		}
		return nkderr.Wrap(code, err, "administrator authentication did not complete; no privileged command was retried")
	}
	return nil
}

type sudoRequest struct {
	ctx    context.Context
	result chan error
}

var errTerminalNotRestored = errors.New("authentication terminal foreground was not restored")

func (w *checkedWriter) SudoAuthentication() func(context.Context) error { return w.sudoAuth }

// Once the presenter accepts a request, its caller waits for this joined result
// even on cancellation. A prompt panic must not strand the workflow callback.
func suspendForAuthentication(ctx context.Context, screen tcell.Screen, authenticate func(context.Context) error) (resumed bool, result error) {
	defer func() {
		if recover() != nil {
			result = errors.Join(result, nkedemo.ErrUnjoined, nkderr.New(nkderr.CodePreflightFailed, "administrator authentication failed unexpectedly; owned cleanup cannot be confirmed"))
		}
	}()
	if err := screen.Suspend(); err != nil {
		return false, nkderr.New(nkderr.CodePreflightFailed, "suspend terminal for administrator authentication failed")
	}
	defer func() {
		if errors.Is(result, errTerminalNotRestored) {
			return
		}
		if err := screen.Resume(); err != nil {
			result = errors.Join(result, nkderr.New(nkderr.CodePreflightFailed, "resume terminal after administrator authentication failed"))
		} else {
			resumed = true
		}
	}()
	return false, authenticate(ctx)
}
