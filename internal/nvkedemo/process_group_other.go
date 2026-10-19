// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package nvkedemo

import (
	"errors"

	"golang.org/x/sys/unix"
)

func groupAlive(pid int) (bool, error) {
	err := unix.Kill(-pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
