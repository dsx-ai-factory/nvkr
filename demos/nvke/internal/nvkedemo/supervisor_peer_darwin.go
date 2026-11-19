// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package nvkedemo

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// Darwin's socket getter re-resolves its last accessor's PID. The generic API
// has no original generation selector, so it cannot issue a safe observation.
func observeSupervisorPeer(context.Context, *net.UnixConn, int) (*SupervisorPeer, error) {
	return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "Darwin peer observation requires the retained owner's original generation")
}

func checkSupervisorPeerCapability(context.Context) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "Darwin peer observation requires the retained owner's original generation")
}

func newDarwinExitQueue(ctx context.Context, pid int) (*os.File, error) {
	if ctx == nil {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "process watch requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "process watch canceled")
	}
	syscall.ForkLock.RLock()
	fd, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "retained-peer-exit")
	change := unix.Kevent_t{Fflags: unix.NOTE_EXIT}
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ENABLE|unix.EV_RECEIPT)
	events := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(fd, []unix.Kevent_t{change}, events, &unix.Timespec{})
	if err == nil && (n != 1 || events[0].Flags&unix.EV_ERROR == 0 || events[0].Data != 0) {
		err = errors.New("kernel did not acknowledge the exact process exit watch")
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func pollSupervisorPeer(file *os.File, timeout time.Duration) (bool, error) {
	events := make([]unix.Kevent_t, 1)
	limit := unix.NsecToTimespec(timeout.Nanoseconds())
	n, err := unix.Kevent(int(file.Fd()), nil, events, &limit)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	if err != nil || n == 0 {
		return false, err
	}
	event := events[0]
	if event.Flags&unix.EV_ERROR != 0 || event.Filter != unix.EVFILT_PROC || event.Ident == 0 || event.Fflags&unix.NOTE_EXIT == 0 {
		return false, errors.New("bound supervisor process watch became uninspectable")
	}
	return true, nil
}
