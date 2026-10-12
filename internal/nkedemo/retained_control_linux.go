// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func retainedRequesterUID(connection *net.UnixConn) uint32 {
	uid := ^uint32(0)
	raw, err := connection.SyscallConn()
	if err != nil {
		return uid
	}
	_ = raw.Control(func(socket uintptr) {
		credentials, err := unix.GetsockoptUcred(int(socket), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil && credentials.Pid > 0 {
			uid = credentials.Uid
		}
	})
	return uid
}

func detachRetainedProcess() error {
	_, err := unix.Setsid()
	if err != nil {
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
