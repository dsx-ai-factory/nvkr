// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package ownedcmd

import (
	"os"
	"os/exec"
	"time"
)

// ConfigureCancellation fails when owned process groups are unavailable.
func ConfigureCancellation(_ *exec.Cmd, _ time.Duration) error {
	return ErrUnsupported
}

// KillDescendants cannot signal a process group on an unsupported host.
func KillDescendants(*exec.Cmd) error { return os.ErrProcessDone }

func cleanupCommandGroup(_ int, _ time.Duration) error {
	return nil
}
