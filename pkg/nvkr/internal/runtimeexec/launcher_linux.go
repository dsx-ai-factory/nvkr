// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

func platformAdmission() error {
	if os.Geteuid() != 0 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return e.New(e.CodeEngineCapability, "runtime namespace setup requires privileged native Linux bootstrap")
	}
	capabilities := [2]unix.CapUserData{}
	if err := unix.Capget(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &capabilities[0]); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "observing namespace setup capabilities")
	}
	for _, capability := range []struct {
		number uint
		name   string
	}{
		{unix.CAP_SYS_ADMIN, "CAP_SYS_ADMIN"}, {unix.CAP_NET_ADMIN, "CAP_NET_ADMIN"},
		{unix.CAP_SYS_CHROOT, "CAP_SYS_CHROOT"}, {unix.CAP_MKNOD, "CAP_MKNOD"},
		{unix.CAP_SETUID, "CAP_SETUID"}, {unix.CAP_SETGID, "CAP_SETGID"},
		{unix.CAP_SETPCAP, "CAP_SETPCAP"}, {unix.CAP_CHOWN, "CAP_CHOWN"},
	} {
		if capabilities[capability.number/32].Effective&(1<<(capability.number%32)) == 0 {
			return e.New(e.CodeEngineCapability, "runtime namespace setup requires %s", capability.name)
		}
	}
	return nil
}

func executeChild(ctx context.Context, self *heldfile.File, record requestRecord, descriptors []*os.File, stdout, stderr io.Writer) error {
	// PDEATHSIG is bound to the creating thread. Keep it alive through Wait.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := platformAdmission(); err != nil {
		return err
	}
	parentFD, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "holding runtime launcher lifetime")
	}
	parent := os.NewFile(uintptr(parentFD), "runtime-parent")
	defer parent.Close()
	raw, err := encodeRequest(record)
	if err != nil {
		return err
	}
	request, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "runtime-namespace-request", MaxBytes: maxRequestBytes})
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding runtime namespace request")
	}
	defer func() { _ = request.Close() }()
	files := []*os.File{self.OSFile(), request.OSFile()}
	for _, descriptor := range descriptors {
		fd, err := unix.FcntlInt(descriptor.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "retaining runtime input descriptor")
		}
		file := os.NewFile(uintptr(fd), "runtime-input")
		defer file.Close()
		files = append(files, file)
	}
	if 3+len(files) != parentDescriptor(record) {
		return e.New(e.CodeEngineCapability, "runtime parent descriptor differs from its fixed slot")
	}
	files = append(files, parent)
	command := exec.CommandContext(ctx, "/proc/self/fd/3", EntryArgument)
	command.ExtraFiles = files
	command.Env = []string{"LANG=C", "LC_ALL=C"}
	command.Dir = "/"
	command.Stdin = nil
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL,
		Cloneflags: unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWNET}
	if record.Mode == "outbound" {
		// Only the fixed descriptor-only network helper retains the outer
		// network namespace. Its root, PID, IPC and UTS remain private.
		command.SysProcAttr.Cloneflags &^= unix.CLONE_NEWNET
	}
	if record.Mode == "assemble" || record.Mode == "probe-assembly" {
		// Mkosi requires /dev/tty in its nested sandbox. Start a new session
		// without a controlling terminal before exposing that fixed API device.
		// setsid also creates the process group used by cancellation below.
		command.SysProcAttr.Setpgid = false
		command.SysProcAttr.Setsid = true
		command.SysProcAttr.Cloneflags |= unix.CLONE_NEWUSER
		command.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: AssemblyHostID, Size: AssemblyIDCount}}
		command.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: AssemblyHostID, Size: AssemblyIDCount}}
		command.SysProcAttr.GidMappingsEnableSetgroups = true
		command.SysProcAttr.Credential = &syscall.Credential{Uid: 0, Gid: 0}
	}
	command.Cancel = func() error {
		err := unix.Kill(-command.Process.Pid, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "starting private runtime namespaces")
	}
	err = command.Wait()
	// Exiting PID 1 destroys the PID namespace, including descendants that
	// changed process groups or sessions. Wait joins that namespace owner.
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "runtime namespace execution canceled")
	}
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "runtime namespace execution failed")
	}
	return nil
}
