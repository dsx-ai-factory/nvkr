// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nkderrors "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestRunnerGitAdmissionNamesFailingPhase(t *testing.T) {
	root := privateTestDir(t)
	checkout := filepath.Join(root, "source")
	runner := Runner{Env: []string{"PATH=" + os.Getenv("PATH")}}
	if err := runner.Run(t.Context(), []string{"git", "init", checkout}, "init-source", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", checkout, "config", "extensions.worktreeConfig", "true")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	cmd.WaitDelay = time.Second
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture config: %v: %s", err, raw)
	}
	runner.Work = root
	_, err := runner.Capture(t.Context(), []string{"git", "-C", checkout, "status", "--porcelain"}, "checkout-installer", 10*time.Second)
	var command *CommandError
	var coded *nkderrors.Error
	if !errors.As(err, &command) || command.Phase != "checkout-installer" || command.ExitCode != -1 ||
		!errors.As(err, &coded) || coded.Code != nkderrors.CodeEngineCapability {
		t.Fatalf("Git admission lost phase or coded cause: %v", err)
	}
	if strings.Contains(err.Error(), checkout) {
		t.Fatal("admission error leaked the source path")
	}
	if _, statErr := os.Stat(filepath.Join(root, "checkout-installer.log")); !os.IsNotExist(statErr) {
		t.Fatal("Git admission created a command log before rejecting configuration")
	}
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunnerCaptureContracts(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		want          []byte
		code          int
		cause         error
	}{
		{"success", "printf hello", []byte("hello"), 0, nil},
		{"binary", "printf '\\377\\000\\001'", []byte{255, 0, 1}, 0, nil},
		{"stderr", "printf error >&2; printf result", []byte("result"), 0, nil},
		{"exit", "printf partial; exit 7", []byte("partial"), 7, nil},
		{"timeout", "printf started; exec sleep 60", []byte("started"), -1, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := privateTestDir(t)
			var progress bytes.Buffer
			runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}, Progress: &progress}
			timeout := 5 * time.Second
			if tc.name == "timeout" {
				timeout = 100 * time.Millisecond
			}
			got, err := runner.Capture(t.Context(), []string{"/bin/sh", "-c", tc.command}, "phase", timeout)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("capture %q, want %q", got, tc.want)
			}
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 && (err == nil || CommandExitCode(err) != tc.code) {
				t.Fatalf("exit=%d, err=%v", CommandExitCode(err), err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("cause not retained: %v", err)
			}
			info, err := os.Stat(filepath.Join(work, "phase.log"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("log mode %o", info.Mode().Perm())
			}
			if strings.Contains(progress.String(), tc.command) {
				t.Fatal("progress leaked arguments")
			}
			if runtime.GOOS == "linux" { // Linux permits checking exact process-group disappearance.
				if errors.Is(err, ErrUnjoined) {
					t.Fatal("owned child was not joined")
				}
			}
		})
	}
}

func TestRunnerRejectsBeforeProcessWork(t *testing.T) {
	for _, name := range []string{"nil context", "canceled", "nil environment", "phase traversal", "workspace symlink", "workspace public", "existing log", "empty command", "zero timeout"} {
		t.Run(name, func(t *testing.T) {
			work := privateTestDir(t)
			marker := filepath.Join(work, "started")
			runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}
			ctx := t.Context()
			phase := "admission"
			args := []string{"/bin/sh", "-c", "touch \"$1\"", "fixture", marker}
			timeout := time.Second
			switch name {
			case "nil context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil environment":
				runner.Env = nil
			case "phase traversal":
				phase = "../outside"
			case "workspace symlink":
				link := filepath.Join(privateTestDir(t), "link")
				if err := os.Symlink(work, link); err != nil {
					t.Fatal(err)
				}
				runner.Work = link
			case "workspace public":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			case "existing log":
				if err := os.WriteFile(filepath.Join(work, phase+".log"), []byte("held"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty command":
				args = nil
			case "zero timeout":
				timeout = 0
			}
			if err := runner.Run(ctx, args, phase, timeout); err == nil {
				t.Fatal("accepted unsafe command input")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("producer ran before admission")
			}
		})
	}
}

func TestRunnerUsesOnlyExplicitEnvironmentAndPATH(t *testing.T) {
	t.Setenv("NICO_DIAGNOSTIC_SECRET", "foreign")
	tool := filepath.Join(privateTestDir(t), "private-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s' \"${NICO_DIAGNOSTIC_SECRET-unset}\""), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Env: []string{"PATH=" + filepath.Dir(tool)}}
	got, err := runner.Capture(t.Context(), []string{"private-tool"}, "env", 5*time.Second)
	if err != nil || string(got) != "unset" {
		t.Fatalf("result=%q err=%v", got, err)
	}
}

func TestRunnerCancellationJoinsAndRetainsPartialLog(t *testing.T) {
	work := privateTestDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	got, err := (Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}).Capture(ctx, []string{"/bin/sh", "-c", "printf started; exec sleep 60"}, "cancel", time.Minute)
	if string(got) != "started" || !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnjoined) {
		t.Fatalf("capture=%q error=%v", got, err)
	}
	log, err := os.ReadFile(filepath.Join(work, "cancel.log"))
	if err != nil || string(log) != "started" {
		t.Fatalf("lost cancellation diagnostics: %q %v", log, err)
	}
}

func TestRunnerBoundedDiagnosticDrainsAndJoins(t *testing.T) {
	work := privateTestDir(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Work: work, Env: []string{"NKD_SUPPORT_HELPER=oversized"}}
	got, err := runner.Capture(t.Context(), []string{program, "-test.run=TestSupportHelperProcess"}, "large", 10*time.Second)
	if err == nil || errors.Is(err, ErrUnjoined) || len(got) != diagnosticLimit {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	info, statErr := os.Stat(filepath.Join(work, "large.log"))
	if statErr != nil || info.Size() > diagnosticLimit {
		t.Fatalf("unbounded log: %v %v", info, statErr)
	}
}

func TestSupportHelperProcess(t *testing.T) {
	mode := os.Getenv("NKD_SUPPORT_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "oversized":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), diagnosticLimit+1024))
		_, _ = fmt.Fprint(os.Stdout, "\n[done] after diagnostic limit\n")
	case "linger":
		time.Sleep(time.Minute)
	case "orphan":
		child := exec.Command(os.Args[0], "-test.run=TestSupportHelperProcess")
		child.Env = []string{"NKD_SUPPORT_HELPER=linger"}
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprint(os.Stdout, child.Process.Pid)
	}
	os.Exit(0)
}

func TestOwnedProcessStopAndJoin(t *testing.T) {
	work := privateTestDir(t)
	p, err := (Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}).Start(t.Context(), []string{"/bin/sh", "-c", "exec sleep 60"}, "server")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if exited, _ := p.Exited(); exited {
		t.Fatal("server exited before stop")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if exited, _ := p.Exited(); !exited {
		t.Fatal("direct child not joined")
	}
	if alive, err := groupAlive(p.PID()); err != nil || alive {
		t.Fatalf("group survives: %v %v", alive, err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal("idempotent stop", err)
	}
}

func TestOwnedProcessCancellationPreservesCleanupLifetime(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprintf("canceled-before-start=%t", before), func(t *testing.T) {
			work := privateTestDir(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if before {
				cancel()
			}
			p, err := (Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}).Start(ctx, []string{"/bin/sh", "-c", "exec sleep 60"}, "server")
			if before {
				if p != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled Start: process=%v error=%v", p, err)
				}
				if _, err := os.Stat(filepath.Join(work, "server.log")); !os.IsNotExist(err) {
					t.Fatal("canceled Start performed process work")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			defer func() {
				if err := p.Stop(cleanup); err != nil {
					t.Error(err)
				}
			}()
			cancel()
			select {
			case <-p.done:
				t.Fatal("work cancellation killed the service before owner cleanup")
			case <-time.After(200 * time.Millisecond):
			}
			if err := p.Stop(cleanup); err != nil {
				t.Fatal(err)
			}
			if alive, err := groupAlive(p.PID()); err != nil || alive {
				t.Fatalf("explicit Stop did not join: alive=%v error=%v", alive, err)
			}
		})
	}
}

func TestOwnedProcessDescendantAfterLeader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("consumer's real orphan-group admission is Linux-only")
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Env: []string{"NKD_SUPPORT_HELPER=orphan"}}
	_, err = runner.Capture(t.Context(), []string{program, "-test.run=TestSupportHelperProcess"}, "orphan", 5*time.Second)
	if errors.Is(err, ErrUnjoined) {
		t.Fatal(err)
	}
	// Cmd.WaitDelay may report a held pipe after the leader exited; cleanup must
	// nevertheless join the descendant before returning this ordinary failure.
}

func TestAuthenticationCommandOwnership(t *testing.T) {
	for _, scenario := range []string{"success", "exit", "cancel", "descendant"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			switch scenario {
			case "exit":
				cmd.Args = []string{"/bin/sh", "-c", "exit 7"}
			case "cancel":
				cmd.Args = []string{"/bin/sh", "-c", "exec sleep 60"}
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			case "descendant":
				if runtime.GOOS != "linux" {
					t.Skip("real orphan-group admission is Linux-only")
				}
				program, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.CommandContext(ctx, program, "-test.run=TestSupportHelperProcess")
				cmd.Env = []string{"NKD_SUPPORT_HELPER=orphan"}
			}
			err := RunAuthenticationCommand(ctx, cmd)
			if errors.Is(err, ErrUnjoined) {
				t.Fatal(err)
			}
			switch scenario {
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case "exit":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("exit lost: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if cmd.Process == nil || cmd.ProcessState == nil {
				t.Fatal("direct authentication child was not joined")
			}
			if alive, err := groupAlive(cmd.Process.Pid); err != nil || alive {
				t.Fatalf("authentication descendants survive: alive=%t error=%v", alive, err)
			}
		})
	}
}

func TestAuthenticationCommandAdmission(t *testing.T) {
	for _, scenario := range []string{"nil-context", "unbounded", "canceled", "nil-command", "not-command-context"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
			switch scenario {
			case "nil-context":
				ctx = nil
			case "unbounded":
				ctx = context.WithoutCancel(ctx)
			case "canceled":
				cancel()
			case "nil-command":
				cmd = nil
			case "not-command-context":
				cmd = exec.Command("/bin/sh", "-c", "exit 0")
			}
			if err := RunAuthenticationCommand(ctx, cmd); err == nil {
				t.Fatal("unsafe authentication command admitted")
			}
			if cmd != nil && cmd.Process != nil {
				t.Fatal("authentication launched before admission")
			}
		})
	}
}

func TestUnjoinedIdentityAndExitClassification(t *testing.T) {
	for _, cause := range []error{ErrUnjoined, context.Canceled, context.DeadlineExceeded} {
		err := errors.Join(&CommandError{Phase: "cleanup", ExitCode: 1, Cause: errors.New("exit")}, cause)
		if CommandExitCode(err) != -1 || !errors.Is(err, cause) {
			t.Fatalf("unsafe exit classification: %v", err)
		}
	}
	p := &OwnedProcess{cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, phase: "fixture", done: make(chan struct{}), groupSignal: func(int, unix.Signal) error { return unix.EPERM }}
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stopCancel()
	if err := p.Stop(stopCtx); !errors.Is(err, ErrUnjoined) {
		t.Fatalf("permission failure lost: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.waitOwned(ctx); !errors.Is(err, ErrUnjoined) || !errors.Is(err, context.Canceled) {
		t.Fatalf("unsafe authentication join lost: %v", err)
	}
}

func TestStopSignalErrorRechecksCompletedJoin(t *testing.T) {
	for _, signal := range []unix.Signal{unix.SIGTERM, unix.SIGKILL} {
		for _, scenario := range []string{
			"joined", "failed-exit", "failed-join", "delayed-joined", "delayed-failed-exit", "delayed-failed-join", "live-permission-error",
			"signal-success-joined", "signal-success-failed-exit", "signal-success-failed-join",
			"signal-success-delayed-joined", "signal-success-delayed-failed-exit", "signal-success-delayed-failed-join",
		} {
			t.Run(fmt.Sprintf("%s/%s", signal, scenario), func(t *testing.T) {
				timeout := 2 * time.Second
				if scenario == "live-permission-error" {
					timeout = 20 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(t.Context(), timeout)
				defer cancel()
				if signal == unix.SIGKILL {
					// Exercise escalation immediately, without a wall-clock wait.
					cancel()
				}
				writer := &recordedProgress{}
				p := &OwnedProcess{
					cmd:      &exec.Cmd{Process: &os.Process{Pid: 123}},
					phase:    "fixture",
					done:     make(chan struct{}),
					progress: beginProgress(writer, "fixture", PhaseStarted, PhaseFinished, PhaseFailed),
				}
				waitErr := errors.New("direct child failed")
				joinErr := fmt.Errorf("%w: finalizer failed", ErrUnjoined)
				outcome := strings.TrimPrefix(scenario, "signal-success-")
				delayed := strings.HasPrefix(outcome, "delayed-")
				outcome = strings.TrimPrefix(outcome, "delayed-")
				complete := func() {
					p.mu.Lock()
					if outcome == "failed-exit" {
						p.waitErr = waitErr
					}
					if outcome == "failed-join" {
						p.joinErr = joinErr
					}
					p.mu.Unlock()
					close(p.done)
				}
				var signals []unix.Signal
				p.groupSignal = func(pid int, got unix.Signal) error {
					if pid != 123 {
						t.Fatalf("signaled a different owner: %d", pid)
					}
					signals = append(signals, got)
					if got != signal {
						return nil
					}
					if scenario != "live-permission-error" {
						// Deterministically complete the finalizer between Stop's
						// pre-signal check and the signal error, or after the error.
						if delayed {
							timer := time.AfterFunc(time.Millisecond, complete)
							t.Cleanup(func() {
								if timer.Stop() {
									complete()
								}
								select {
								case <-p.done:
								case <-time.After(time.Second):
									t.Fatal("fixture finalizer did not join")
								}
							})
						} else {
							complete()
						}
					}
					if strings.HasPrefix(scenario, "signal-success-") {
						return nil
					}
					return unix.EPERM
				}
				err := p.Stop(ctx)
				wantSignals := []unix.Signal{unix.SIGTERM}
				if signal == unix.SIGKILL {
					wantSignals = append(wantSignals, unix.SIGKILL)
				}
				if len(signals) != len(wantSignals) {
					t.Fatalf("signal sequence: %v", signals)
				}
				for i, want := range wantSignals {
					if signals[i] != want {
						t.Fatalf("signal sequence: %v", signals)
					}
				}
				switch outcome {
				case "joined", "failed-exit":
					if err != nil {
						t.Fatalf("completed join replaced by signal failure: %v", err)
					}
				case "failed-join":
					if err != joinErr {
						t.Fatalf("exact finalizer failure lost: %v", err)
					}
				case "live-permission-error":
					if !errors.Is(err, ErrUnjoined) || !errors.Is(err, unix.EPERM) {
						t.Fatalf("live owner's permission failure lost: %v", err)
					}
				}
				wantLast := PhaseFailed
				if outcome == "joined" {
					wantLast = PhaseFinished
				}
				if len(writer.events) != 2 || writer.events[1].Kind != wantLast {
					t.Fatalf("failed direct exit or join presented as success: %v", writer.events)
				}
			})
		}
	}
}

func TestStopSignalErrorCanceledDeadlineRetainsOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := &OwnedProcess{
		cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, done: make(chan struct{}),
		groupSignal: func(int, unix.Signal) error { return unix.EPERM },
	}
	if err := p.Stop(ctx); !errors.Is(err, ErrUnjoined) || !errors.Is(err, unix.EPERM) {
		t.Fatalf("unjoined canceled owner lost signal error: %v", err)
	}
}
