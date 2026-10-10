// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package pipeline

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecCmdCancellationReapsDescendantProcessGroup(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "command")
	source := "#!/bin/sh\n" +
		"sh -c 'trap \"\" TERM; printf \"%s\\n\" \"$$\" >&3; exec 3>&-; while :; do sleep 1; done' &\n" +
		"wait\n"
	if err := os.WriteFile(script, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	defer readyWrite.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := execCmd(RunOptions{Ctx: ctx}, script)
	cmd.ExtraFiles = []*os.File{readyWrite}
	cmd.WaitDelay = 100 * time.Millisecond
	result := make(chan error, 1)
	go func() { result <- runOwnedCommand(ctx, cmd) }()
	if err := readyRead.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		cancel()
		<-result
		t.Fatal(err)
	}
	rawPID, err := bufio.NewReader(readyRead).ReadString('\n')
	if err != nil {
		cancel()
		<-result
		t.Fatalf("descendant process did not become ready: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)

	cancel()
	select {
	case err := <-result:
		_ = readyWrite.Close()
		if err == nil {
			t.Fatal("canceled command with a descendant succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled command did not return within its escalation bound")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived command cancellation", pid)
}

func TestExecCmdSuccessfulChildCannotLeaveDescendant(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "descendant.pid")
	script := filepath.Join(root, "command")
	source := "#!/bin/sh\n" +
		"sh -c 'trap \"\" TERM; echo $$ > \"$NVKR_DESCENDANT_PID\"; while :; do sleep 1; done' &\n" +
		"while [ ! -s \"$NVKR_DESCENDANT_PID\" ]; do sleep 0.01; done\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NVKR_DESCENDANT_PID", pidPath)
	if err := runOwnedCommand(context.Background(), execCmd(RunOptions{}, script)); err != nil {
		t.Fatalf("successful direct child returned %v", err)
	}
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived successful direct-child exit", pid)
}
