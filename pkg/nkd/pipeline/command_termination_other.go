// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package pipeline

import (
	"os"
	"os/exec"
	"time"
)

func configureCommandCancellation(cmd *exec.Cmd, grace time.Duration) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = grace
}

func killCommandGroup(*exec.Cmd) error { return os.ErrProcessDone }
