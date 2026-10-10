// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

func restoreTerminalForeground(fd, foreground int) (result error) {
	runtime.LockOSThread()
	var block, previous unix.Sigset_t
	// SIGTTOU fits the first word on all supported Linux architectures.
	block.Val[0] = 1 << (uint(unix.SIGTTOU) - 1)
	if err := unix.PthreadSigmask(unix.SIG_BLOCK, &block, &previous); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer func() {
		if err := unix.PthreadSigmask(unix.SIG_SETMASK, &previous, nil); err != nil {
			// Do not return a thread with a changed signal mask to Go's pool.
			result = errors.Join(result, err)
			return
		}
		runtime.UnlockOSThread()
	}()
	return unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, foreground)
}
