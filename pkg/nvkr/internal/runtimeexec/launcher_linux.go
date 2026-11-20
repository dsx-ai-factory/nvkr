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
	return executeChildObserved(ctx, self, record, descriptors, stdout, stderr, nil)
}

func executeChildObserved(ctx context.Context, self *heldfile.File, record requestRecord, descriptors []*os.File, stdout, stderr io.Writer, exited func()) error {
	stdout, stderr, err := Outputs(stdout, stderr)
	if err != nil {
		return err
	}
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
	out, err := prepareChildOutput(stdout)
	if err != nil {
		return err
	}
	defer out.close()
	errout, err := prepareChildOutput(stderr)
	if err != nil {
		return err
	}
	defer errout.close()
	if out.file != nil {
		command.Stdout = out.file
	}
	if errout.file != nil {
		command.Stderr = errout.file
	}
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
	out.started()
	errout.started()
	err = command.Wait()
	canceled := ctx.Err()
	// No exec-managed copy goroutine owns these streams. Report process exit
	// before waiting for capture drainage, so helper failure wins independently
	// of diagnostic volume or a blocked consumer output descriptor.
	if exited != nil {
		exited()
	}
	err = errors.Join(err, out.finish(), errout.finish())
	// Exiting PID 1 destroys the PID namespace, including descendants that
	// changed process groups or sessions. Wait joins that namespace owner.
	if canceled != nil {
		return e.Wrap(e.CodeTimeout, canceled, "runtime namespace execution canceled")
	}
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "runtime namespace execution failed")
	}
	return nil
}

// Only admitted memory sinks use a parent-side pipe. Files remain inherited
// descriptors and are never hidden behind a writer or mutex.
type childOutput struct {
	file   *os.File
	reader *os.File
	done   chan error
}

func prepareChildOutput(writer io.Writer) (*childOutput, error) {
	if writer == nil {
		return &childOutput{}, nil
	}
	if file, ok := writer.(*os.File); ok {
		return &childOutput{file: file}, nil
	}
	reader, file, err := os.Pipe()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening runtime capture pipe")
	}
	output := &childOutput{file: file, reader: reader, done: make(chan error, 1)}
	go func() {
		_, err := io.Copy(writer, reader)
		output.done <- err
	}()
	return output, nil
}

func (output *childOutput) started() {
	if output.reader != nil {
		_ = output.file.Close()
	}
}

func (output *childOutput) finish() error {
	if output.done == nil {
		return nil
	}
	defer func() { output.done = nil }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-output.done:
		return err
	case <-timer.C:
		_ = output.reader.Close()
		return errors.Join(exec.ErrWaitDelay, <-output.done)
	}
}

func (output *childOutput) close() {
	if output.reader != nil {
		_ = output.file.Close()
		_ = output.reader.Close()
		if output.done != nil {
			<-output.done //nolint:errcheck // Cleanup deliberately closes the pipe; join its reader without replacing the primary error.
		}
	}
}
