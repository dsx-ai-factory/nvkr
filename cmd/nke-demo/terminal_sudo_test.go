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
	"strings"
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
	suspends, resumes, pumps atomic.Int32
}

func (s *authenticationScreen) Suspend() error {
	s.suspends.Add(1)
	return s.SimulationScreen.Suspend()
}
func (s *authenticationScreen) Resume() error {
	s.resumes.Add(1)
	return s.SimulationScreen.Resume()
}
func (s *authenticationScreen) ChannelEvents(events chan<- tcell.Event, quit <-chan struct{}) {
	s.pumps.Add(1)
	s.testDashboardScreen.ChannelEvents(events, quit)
}

func TestDashboardAuthenticationNeverSuspends(t *testing.T) {
	for _, mode := range []string{"twice", "auth-failure", "canceled-auth-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			screen := &authenticationScreen{testDashboardScreen: &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}}
			view := newDashboard()
			var calls atomic.Int32
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			writer.sudoAuth = func(authCtx context.Context) error {
				n := calls.Add(1)
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
			if screen.suspends.Load() != 0 || screen.resumes.Load() != 0 || screen.pumps.Load() != 1 {
				t.Fatal("noninteractive authentication changed terminal lifecycle")
			}
			if mode == "twice" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("two auth cycles failed: %v calls=%d pumps=%d", err, calls.Load(), screen.pumps.Load())
				}
			} else if err == nil {
				t.Fatal("authentication failure accepted")
			}
			if mode == "canceled-auth-cleanup" && calls.Load() != 2 {
				t.Fatal("canceled work prevented independently bounded cleanup authentication")
			}
		})
	}
}

func TestSudoSessionAdmission(t *testing.T) {
	for _, mode := range []string{"authenticate-fails", "refresh-fails", "success"} {
		t.Run(mode, func(t *testing.T) {
			cause := nkderr.New(nkderr.CodePreflightFailed, "session admission rejected")
			var order []string
			err := withSudoSession(t.Context(), func(context.Context) error {
				order = append(order, "authenticate")
				if mode == "authenticate-fails" {
					return cause
				}
				return nil
			}, func(context.Context) error {
				order = append(order, "refresh")
				if mode == "refresh-fails" {
					return cause
				}
				return nil
			}, time.Hour, func(context.Context, func(context.Context) error) error {
				order = append(order, "work")
				return nil
			})
			want := "authenticate,refresh,work"
			switch mode {
			case "authenticate-fails":
				want = "authenticate"
			case "refresh-fails":
				want = "authenticate,refresh"
			}
			if strings.Join(order, ",") != want {
				t.Fatalf("admission order %v, want %s", order, want)
			}
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, cause) {
				t.Fatalf("admission error lost: %v", err)
			}
		})
	}
}

func TestSudoSessionRefreshOwnership(t *testing.T) {
	for _, mode := range []string{"return", "cancel", "panic", "refresh-fails", "unjoined", "panic-unjoined"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started, joined := make(chan struct{}), make(chan struct{})
			cause := nkderr.New(nkderr.CodePreflightFailed, "session revoked")
			var calls atomic.Int32
			err := withSudoSession(ctx, func(context.Context) error { return nil }, func(refreshCtx context.Context) error {
				if calls.Add(1) == 1 {
					return nil
				}
				close(started)
				defer close(joined)
				if mode == "refresh-fails" {
					return cause
				}
				<-refreshCtx.Done()
				if mode == "unjoined" || mode == "panic-unjoined" {
					return errors.Join(refreshCtx.Err(), nkedemo.ErrUnjoined)
				}
				return refreshCtx.Err()
			}, time.Millisecond, func(workCtx context.Context, refresh func(context.Context) error) error {
				select {
				case <-started:
				case <-workCtx.Done():
					return workCtx.Err()
				}
				switch mode {
				case "panic", "panic-unjoined":
					panic("private workflow panic")
				case "cancel":
					cancel()
					return workCtx.Err()
				case "refresh-fails":
					<-workCtx.Done()
					return workCtx.Err()
				default:
					return nil
				}
			})
			select {
			case <-joined:
			default:
				t.Fatal("sudo refresh was not joined before returning")
			}
			switch mode {
			case "return":
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case "panic":
				assertCode(t, err, nkderr.CodePreflightFailed)
			case "refresh-fails":
				if !errors.Is(err, cause) {
					t.Fatalf("refresh failure lost: %v", err)
				}
			case "unjoined", "panic-unjoined":
				if !errors.Is(err, nkedemo.ErrUnjoined) {
					t.Fatalf("unjoined subprocess failure lost: %v", err)
				}
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

func TestTerminalAuthenticationFailureExplainsDeadline(t *testing.T) {
	for _, row := range []struct {
		name, message string
		code          nkderr.Code
	}{
		{"deadline", "authentication deadline expired", nkderr.CodeTimeout},
		{"canceled", "authentication canceled", nkderr.CodeTimeout},
		{"rejected", "authentication failed", nkderr.CodePreflightFailed},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := t.Context()
			switch row.name {
			case "deadline":
				bounded, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = bounded
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			cause := errors.New("authentication child stopped")
			err := terminalAuthenticationFailure(ctx, cause)
			assertCode(t, err, row.code)
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), row.message) || !strings.Contains(err.Error(), "no privileged command was retried") {
				t.Fatalf("authentication diagnosis lost: %v", err)
			}
		})
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

// Opt-in read-only sudo-cache contract in one real Linux terminal session.
// A NOPASSWD host can pass this test; it does not prove password policy.
func TestSudoSessionTerminalContract(t *testing.T) {
	if os.Getenv("NKD_TEST_SUDO_SESSION") != "1" || runtime.GOOS != "linux" {
		t.Skip("requires an isolated real Linux terminal and explicit sudo-cache test opt-in")
	}
	if terminalSudoAuthenticator(os.Stdout, os.Stderr) == nil {
		t.Fatal("sudo-cache contract requires actual terminal stdin, stdout and stderr")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var checks atomic.Int32
	refreshed := make(chan struct{})
	err := withSudoSession(ctx, authenticateTerminalSudo, func(refreshCtx context.Context) error {
		if err := refreshTerminalSudo(refreshCtx); err != nil {
			return err
		}
		if checks.Add(1) == 2 {
			close(refreshed)
		}
		return nil
	}, 50*time.Millisecond, func(workCtx context.Context, refresh func(context.Context) error) error {
		select {
		case <-workCtx.Done():
			return workCtx.Err()
		case <-refreshed:
			return refresh(workCtx)
		}
	})
	if err != nil || checks.Load() < 3 {
		t.Fatalf("same-terminal cache admission/refresh failed: checks=%d error=%v", checks.Load(), err)
	}
}

// Exercise the real stdio reader and one owned event pump, not SimulationScreen.
// Noninteractive authentication checks must not suspend or restart the screen.
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
