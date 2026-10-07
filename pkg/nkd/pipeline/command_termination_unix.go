// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package pipeline

import (
	"os/exec"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
)

func configureCommandCancellation(cmd *exec.Cmd, grace time.Duration) {
	// The platform build constraint admits the shared process-group primitive.
	_ = ownedcmd.ConfigureCancellation(cmd, grace)
}

func killCommandGroup(cmd *exec.Cmd) error {
	return ownedcmd.KillDescendants(cmd)
}
