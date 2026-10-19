// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package nvkedemo

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Query the kernel's exact process-group membership rather than using signal
// permission as a liveness oracle. Unknown membership remains an error.
func groupAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, errors.New("owned process group requires a positive PID")
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if err != nil {
		return false, err
	}
	return len(members) != 0, nil
}
