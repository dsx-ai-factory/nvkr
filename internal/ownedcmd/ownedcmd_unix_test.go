// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package ownedcmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCommandStartFailurePreservesPathError(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, string) error
	}{
		{"run", func(ctx context.Context, path string) error { return Run(ctx, exec.CommandContext(ctx, path)) }},
		{"output", func(ctx context.Context, path string) error { _, err := Output(ctx, path); return err }},
		{"combined output", func(ctx context.Context, path string) error { _, err := CombinedOutput(ctx, path); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t.Context(), filepath.Join(t.TempDir(), "missing-command")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("start failure lost its path error: %v", err)
			}
		})
	}
}

func TestRunStreamsConfiguredCommandOutput(t *testing.T) {
	var output bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "printf streamed")
	cmd.Stdout = &output
	if err := Run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if output.String() != "streamed" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunRejectsCommandWithoutContextOwnership(t *testing.T) {
	if err := Run(context.Background(), exec.Command("sh", "-c", "exit 0")); err == nil {
		t.Fatal("contextless command unexpectedly ran")
	}
}

func TestOutputCleansDescendantsAfterSuccessfulParent(t *testing.T) {
	out, err := Output(context.Background(), "sh", "-c",
		"sleep 30 >/dev/null 2>&1 & printf '%s' \"$!\"")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("child pid %q: %v", out, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("successful parent left descendant pid %d running", pid)
}

func TestCombinedOutputPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := CombinedOutput(ctx, "sh", "-c", "trap '' TERM; sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled command error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled command returned after %s", elapsed)
	}
}

func TestOutputRejectsNilContextBeforeStart(t *testing.T) {
	//nolint:staticcheck // The exported boundary must reject a nil context before starting work.
	if _, err := Output(nil, "must-not-run"); err == nil {
		t.Fatal("nil context unexpectedly started a command")
	}
}

func TestRunOwnsDetachedSessionAndCancellation(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(strconv.FormatBool(detached), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			if err := reader.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, "/bin/sh", "-c", "trap '' TERM; printf '%s\\n' \"$$\"; sleep 30 & wait")
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: detached}
			command.Stdout = writer
			joined := make(chan error, 1)
			go func() { joined <- Run(ctx, command) }()
			defer func() {
				cancel()
				if joined != nil {
					<-joined
				}
			}()
			line, err := bufio.NewReader(reader).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}
			if group, err := syscall.Getpgid(pid); err != nil || group != pid {
				t.Fatalf("child group = %d, %v; want PID %d", group, err, pid)
			}
			session, err := unix.Getsid(pid)
			if err != nil || (session == pid) != detached {
				t.Fatalf("child session = %d, %v; detached = %t", session, err, detached)
			}
			cancel()
			err = <-joined
			joined = nil
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled command = %v", err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("canceled command left process group %d", pid)
		})
	}
}
