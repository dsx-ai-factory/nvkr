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
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/sys/unix"
)

type authenticationScreen struct {
	*testDashboardScreen
	suspendFailure, resumeFailure bool
	suspends, resumes, pumps      atomic.Int32
}

func (s *authenticationScreen) Suspend() error {
	s.suspends.Add(1)
	if s.suspendFailure {
		return errors.New("private suspend error")
	}
	return s.SimulationScreen.Suspend()
}
func (s *authenticationScreen) Resume() error {
	s.resumes.Add(1)
	if s.resumeFailure {
		return errors.New("private resume error")
	}
	return s.SimulationScreen.Resume()
}
func (s *authenticationScreen) ChannelEvents(events chan<- tcell.Event, quit <-chan struct{}) {
	s.pumps.Add(1)
	s.testDashboardScreen.ChannelEvents(events, quit)
}

func TestDashboardAuthenticationOwnsSuspendAndResume(t *testing.T) {
	for _, mode := range []string{"twice", "auth-failure", "suspend-failure", "resume-failure", "foreground-not-restored", "canceled-auth-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &authenticationScreen{testDashboardScreen: &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}, suspendFailure: mode == "suspend-failure", resumeFailure: mode == "resume-failure"}
			view := newDashboard()
			var calls atomic.Int32
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			writer.sudoAuth = func(authCtx context.Context) error {
				n := calls.Add(1)
				if mode == "foreground-not-restored" {
					return errors.Join(errTerminalNotRestored, nkderr.New(nkderr.CodePreflightFailed, "terminal restore failed"))
				}
				if mode == "auth-failure" {
					return nkderr.New(nkderr.CodePreflightFailed, "authentication rejected")
				}
				if mode == "canceled-auth-cleanup" && n == 1 {
					cancel()
					return authCtx.Err()
				}
				return authCtx.Err()
			}
			joined := false
			err := dashboardLoop(ctx, cancel, screen, func() error { return nil }, writer, view, func(workCtx context.Context, progress io.Writer) error {
				defer func() { joined = true }()
				auth := progress.(nkedemo.SudoAuthorizer).SudoAuthentication()
				first := auth(workCtx)
				if mode == "twice" && first == nil {
					return auth(workCtx)
				}
				if mode == "canceled-auth-cleanup" {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(workCtx), time.Second)
					defer stop()
					return errors.Join(first, auth(cleanup))
				}
				return first
			})
			if !joined || !screen.joined.Load() {
				t.Fatal("worker or terminal did not join")
			}
			if mode == "twice" {
				if err != nil || calls.Load() != 2 || screen.suspends.Load() != 2 || screen.resumes.Load() != 2 || screen.pumps.Load() != 3 {
					t.Fatalf("two auth cycles failed: %v calls=%d pumps=%d", err, calls.Load(), screen.pumps.Load())
				}
			} else if err == nil {
				t.Fatal("authentication failure accepted")
			}
			if mode == "suspend-failure" && calls.Load() != 0 {
				t.Fatal("prompt ran with raw terminal")
			}
			if mode == "foreground-not-restored" && screen.resumes.Load() != 0 {
				t.Fatal("resumed terminal drawing without foreground ownership")
			}
			if mode == "canceled-auth-cleanup" && calls.Load() != 2 {
				t.Fatal("canceled work prevented independently bounded cleanup authentication")
			}
		})
	}
}

func TestRedirectedSudoDoesNotOfferPrompt(t *testing.T) {
	var redirected bytes.Buffer
	for _, streams := range [][2]io.Writer{{&redirected, os.Stderr}, {os.Stdout, &redirected}, {&redirected, &redirected}} {
		if terminalSudoAuthenticator(streams[0], streams[1]) != nil {
			t.Fatal("redirected stream enabled password prompt")
		}
	}
}

func TestForegroundAuthenticationRejectsNonTerminal(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	err := runForegroundAuthentication(t.Context(), cmd, -1)
	assertCode(t, err, nkderr.CodePreflightFailed)
	if cmd.Process != nil {
		t.Fatal("child started before terminal admission")
	}
}

// Opt-in real terminal contract: invoke the compiled test binary under script.
// This tests process groups/terminal restoration, not a real sudo password policy.
func TestForegroundAuthenticationTerminalContract(t *testing.T) {
	if os.Getenv("NKD_TEST_TERMINAL_AUTH") != "1" || runtime.GOOS != "linux" {
		t.Skip("requires an isolated real Linux terminal")
	}
	fd := int(os.Stdin.Fd())
	before, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			command := "exit 0"
			if mode == "canceled" {
				command = "sleep 30"
			}
			cmd := exec.CommandContext(ctx, "sh", "-c", command)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			err := runForegroundAuthentication(ctx, cmd, fd)
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			if mode == "canceled" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation lost: %v", err)
			}
			after, readErr := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
			if readErr != nil || before != after {
				t.Fatalf("foreground not restored: %d -> %d (%v)", before, after, readErr)
			}
		})
	}
}

// Exercise the real stdio reader/Drain/Stop/Start cycle, not SimulationScreen.
// No privileged command or deployment is involved in these presentation checks.
func TestDashboardAuthenticationTerminalContract(t *testing.T) {
	if os.Getenv("NKD_TEST_TERMINAL_AUTH") != "1" {
		t.Skip("requires an isolated real terminal")
	}
	if !dashboardEligible(os.Stdout, false, false) {
		t.Fatal("real dashboard terminal is required; plain fallback cannot prove this contract")
	}
	for _, mode := range []string{"noop", "owned-command"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			inputFD := int(os.Stdin.Fd())
			inputFlags, err := unix.FcntlInt(uintptr(inputFD), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			err = presentProgress(ctx, os.Stdout, nil, false, "", false, func(workCtx context.Context, progress io.Writer) error {
				auth := progress.(nkedemo.SudoAuthorizer).SudoAuthentication()
				for range 2 {
					if err := auth(workCtx); err != nil {
						return err
					}
					timer := time.NewTimer(100 * time.Millisecond)
					select {
					case <-workCtx.Done():
						timer.Stop()
						return workCtx.Err()
					case <-timer.C:
					}
				}
				return nil
			}, func(authCtx context.Context) error {
				calls.Add(1)
				if mode == "noop" {
					return authCtx.Err()
				}
				cmd := exec.CommandContext(authCtx, "/bin/sh", "-c", "exit 0")
				cmd.Env = nkedemo.NativeEnvironment()
				return nkedemo.RunAuthenticationCommand(authCtx, cmd)
			})
			if err != nil || calls.Load() != 2 {
				t.Fatalf("real dashboard auth cycle failed: calls=%d error=%v", calls.Load(), err)
			}
			flags, err := unix.FcntlInt(uintptr(inputFD), unix.F_GETFL, 0)
			if err != nil || flags != inputFlags {
				t.Fatalf("terminal flags changed: before=%d after=%d error=%v", inputFlags, flags, err)
			}
		})
	}
}

func TestTerminalFlagRestorationFailureStopsWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tty := &checkedTTY{cancel: cancel, inputFD: -1, restoreInputFlags: true}
	if n, err := tty.Write([]byte("must not reach a terminal")); n != 0 || !errors.Is(err, unix.EBADF) {
		t.Fatalf("terminal flags failure ignored: n=%d error=%v", n, err)
	}
	if ctx.Err() == nil {
		t.Fatal("terminal flags failure did not cancel work")
	}
	assertCode(t, tty.failure(), nkderr.CodePreflightFailed)
}
