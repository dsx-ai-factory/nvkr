// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package heldfile

import (
	"os"
	"os/exec"
	"time"
)

// Executable capabilities fail before command construction on these platforms.
func configureCommandCancellation(*exec.Cmd, time.Duration) {}

func killCommandGroup(*exec.Cmd) error { return os.ErrProcessDone }
