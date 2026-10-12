// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func startRetainedChild(ctx context.Context, command *exec.Cmd) (*retainedChild, error) {
	if command == nil || command.Process != nil {
		return nil, nkderr.New(nkderr.CodeUsage, "retained startup requires a new fixed child command")
	}
	for _, stream := range []any{command.Stdin, command.Stdout, command.Stderr} {
		if stream != nil {
			if _, ok := stream.(*os.File); !ok {
				return nil, nkderr.New(nkderr.CodeUsage, "retained startup requires file-backed stdio, not copy goroutines")
			}
		}
	}
	if err := CheckSupervisorPeerCapability(ctx); err != nil {
		return nil, err
	}
	pidfd := -1
	// Go's existing Linux spawn requests CLONE_PIDFD atomically. Do not set a
	// process group here: the owner must perform/acknowledge its own later setsid.
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	if err := command.Start(); err != nil {
		return nil, nkderr.Wrap(nkderr.CodePreflightFailed, err, "start retained service owner")
	}
	if pidfd < 0 {
		return &retainedChild{command: command}, nkderr.New(nkderr.CodeEngineCapability, "spawn did not supply required creation-time process handle; no service startup requested")
	}
	return &retainedChild{command: command, exit: os.NewFile(uintptr(pidfd), "retained-created-child")}, nil
}

func (child *retainedChild) release() error {
	if child == nil || child.command == nil || child.command.Process == nil || child.exit == nil {
		return nkderr.New(nkderr.CodeUsage, "retained release requires its actual child creation handle")
	}
	if child.released {
		return nil
	}
	pid := child.command.Process.Pid
	sid, err := unix.Getsid(pid)
	if err != nil || sid != pid {
		return nkderr.New(nkderr.CodePreflightFailed, "retained child has not supplied its native detached session")
	}
	group, err := unix.Getpgid(pid)
	if err != nil || group != pid {
		return nkderr.New(nkderr.CodePreflightFailed, "retained child has not supplied its native detached process group")
	}
	if err := child.command.Process.Release(); err != nil {
		return nkderr.Wrap(nkderr.CodePreflightFailed, err, "release acknowledged retained child handle")
	}
	child.released = true
	return child.exit.Close()
}

// Only the actual directly spawned, unreaped pre-start child can reach this
// rare capability-refusal join. No endpoint description, caller PID or runtime
// service is accepted. WNOWAIT reserves the child identity until its sole Wait.
func joinRetainedPrestart(ctx context.Context, command *exec.Cmd) error {
	if ctx == nil || command == nil || command.Process == nil {
		return nkderr.New(nkderr.CodeUsage, "pre-start join requires the actual child")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nkderr.New(nkderr.CodeUsage, "pre-start join requires a deadline")
	}
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrUnjoined, nkderr.Wrap(nkderr.CodeTimeout, err, "pre-start child did not join"))
		}
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, command.Process.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err != nil && err != unix.EINTR {
			return errors.Join(ErrUnjoined, nkderr.Wrap(nkderr.CodePreflightFailed, err, "observe original unreaped pre-start child"))
		}
		if err == nil && info.Signo == int32(unix.SIGCHLD) {
			return command.Wait()
		}
		if err := pause(ctx, 20*time.Millisecond); err != nil {
			return errors.Join(ErrUnjoined, err)
		}
	}
}
