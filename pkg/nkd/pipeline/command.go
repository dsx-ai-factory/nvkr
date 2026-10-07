// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// execCmd builds a context-aware subprocess that, on ctx cancel, is sent SIGTERM and
// then SIGKILL after a grace delay (ADR-17 §2), leaving no runaway process.
func execCmd(opts RunOptions, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(opts.ctx(), name, args...)
	configureCommandCancellation(cmd, 10*time.Second)
	return cmd
}

// runOwnedCommand joins the direct child and then force-kills any descendants in
// its private process group. This is required even after a successful direct-child
// exit: qualified commands may not daemonize or transfer process ownership.
func runOwnedCommand(ctx context.Context, cmd *exec.Cmd) error {
	err := cmd.Run()
	cleanupErr := killCommandGroup(cmd)
	if err != nil {
		return err
	}
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return cleanupErr
	}
	return nil
}

func outputOwnedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	cleanupErr := killCommandGroup(cmd)
	if err != nil {
		return out, err
	}
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return out, cleanupErr
	}
	return out, nil
}
