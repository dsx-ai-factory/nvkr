// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package heldfile

import (
	"os/exec"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func configureCommandCancellation(cmd *exec.Cmd, grace time.Duration) {
	// The platform build constraint admits the shared process-group primitive.
	_ = ownedcmd.ConfigureCancellation(cmd, grace)
}

// killCommandGroup reaps descendants which outlive the direct child after a
// cancellation. exec.Cmd's WaitDelay escalation kills only cmd.Process.
func killCommandGroup(cmd *exec.Cmd) error {
	return ownedcmd.KillDescendants(cmd)
}
