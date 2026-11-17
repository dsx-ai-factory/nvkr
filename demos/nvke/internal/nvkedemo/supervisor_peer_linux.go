// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvkedemo

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func observeSupervisorPeer(ctx context.Context, connection *net.UnixConn, expectedPID int) (*SupervisorPeer, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "inspect supervisor socket")
	}
	var peer *SupervisorPeer
	var observedErr error
	err = raw.Control(func(socket uintptr) {
		if observedErr = ctx.Err(); observedErr != nil {
			return
		}
		credentials, readErr := unix.GetsockoptUcred(int(socket), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if readErr != nil {
			observedErr = readErr
			return
		}
		if credentials.Uid != uint32(os.Getuid()) || credentials.Pid <= 0 || int(credentials.Pid) != expectedPID {
			observedErr = errors.New("supervisor socket peer does not match the current user and selected child")
			return
		}
		fd, readErr := unix.GetsockoptInt(int(socket), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		if readErr != nil {
			observedErr = nvkrerr.Wrap(nvkrerr.CodeEngineCapability, readErr, "Linux connected-peer process handles are required; no PID-only fallback")
			return
		}
		if fd < 0 {
			observedErr = errors.New("kernel returned an invalid supervisor peer handle")
			return
		}
		unix.CloseOnExec(fd)
		peer = &SupervisorPeer{pid: int(credentials.Pid), uid: credentials.Uid, file: os.NewFile(uintptr(fd), "supervisor-peer")}
	})
	if err != nil || observedErr != nil {
		if peer != nil {
			_ = peer.Close()
		}
		combined := errors.Join(err, observedErr)
		var coded *nvkrerr.Error
		if errors.As(combined, &coded) {
			return nil, combined
		}
		if ctx.Err() != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, combined, "supervisor peer observation canceled")
		}
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, combined, "supervisor peer binding failed")
	}
	return peer, nil
}

func checkSupervisorPeerCapability(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "supervisor capability check canceled")
	}
	// A socketpair created here correctly describes this process. Real daemon
	// observation must use a listener created by that daemon, not this pair.
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "native supervisor socket capability unavailable")
	}
	defer func() { _ = unix.Close(sockets[0]); _ = unix.Close(sockets[1]) }()
	fd, err := unix.GetsockoptInt(sockets[0], unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "Linux connected-peer process handles are required; no PID-only fallback")
	}
	if err := unix.Close(fd); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "close native supervisor capability observation")
	}
	return nil
}

func pollSupervisorPeer(file *os.File, timeout time.Duration) (bool, error) {
	rows := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	_, err := unix.Poll(rows, int(timeout/time.Millisecond))
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rows[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		return false, errors.New("bound supervisor process handle became uninspectable")
	}
	return rows[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
}
