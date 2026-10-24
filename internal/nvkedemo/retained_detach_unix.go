// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package nvkedemo

import (
	"os"

	"golang.org/x/sys/unix"
)

func detachRetainedProcess() error {
	if _, err := unix.Setsid(); err != nil {
		return err
	}
	sid, err := unix.Getsid(os.Getpid())
	if err != nil {
		return err
	}
	if sid != os.Getpid() || unix.Getpgrp() != os.Getpid() {
		return unix.EPERM
	}
	return nil
}
