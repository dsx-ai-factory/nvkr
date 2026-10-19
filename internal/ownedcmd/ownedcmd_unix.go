// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package ownedcmd

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ConfigureCancellation gives cmd its own process group and sends SIGTERM to
// that group when its context is canceled. The caller chooses the grace period
// and retains ownership of Start, Wait, and post-wait descendant cleanup.
func ConfigureCancellation(cmd *exec.Cmd, grace time.Duration) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// setsid already creates a private group with PGID equal to the child PID.
	// Combining it with setpgid makes session creation fail with EPERM.
	cmd.SysProcAttr.Setpgid = !cmd.SysProcAttr.Setsid
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return signalCommandGroup(cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = grace
	return nil
}

// KillDescendants force-stops the private process group after the caller joins
// its direct child. It does not wait, release held inputs, or classify errors.
func KillDescendants(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return signalCommandGroup(cmd.Process.Pid, syscall.SIGKILL)
}

func signalCommandGroup(pgid int, signal syscall.Signal) error {
	err := syscall.Kill(-pgid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func cleanupCommandGroup(pgid int, grace time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if err := signalCommandGroup(pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		err := signalCommandGroup(pgid, 0)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := signalCommandGroup(pgid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
