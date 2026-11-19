// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package nvkedemo

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// An unattended owner needs no controlling terminal and must survive loss of
// the frontend process group while deployment is still starting. Interactive
// sudo keeps its original terminal until the normal held-state handoff.
func isolateRetainedCommand(command *exec.Cmd, terminalAuthentication bool) {
	if !terminalAuthentication {
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

func detachRetainedProcess() error {
	if sid, err := unix.Getsid(os.Getpid()); err == nil && sid == os.Getpid() && unix.Getpgrp() == os.Getpid() {
		return nil
	}
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
