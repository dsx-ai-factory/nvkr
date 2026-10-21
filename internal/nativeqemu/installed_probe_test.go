// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package nativeqemu

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInstalledStandardTPMDevice(t *testing.T) {
	if os.Getenv("NVKR_NATIVE_QEMU_REAL_PROBE") != "1" {
		t.Skip("explicit installed-tool diagnostic; no guest boot or attestation claim")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"simulator", "simulator/instances", "simulator/instances/instance"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	qemu, err := exec.LookPath("qemu-system-aarch64")
	if err != nil {
		t.Fatal(err)
	}
	swtpm, err := exec.LookPath("swtpm")
	if err != nil {
		t.Fatal(err)
	}
	// Match the demo's reduced native environment; installed libraries must not
	// depend on an interactive HOME or an injected dynamic-loader setting.
	environment := []string{"PATH=" + os.Getenv("PATH"), "USER=" + os.Getenv("USER"), "LANG=C"}
	adapter, err := prepare(t.Context(), Options{self, filepath.Join(root, "adapter"), filepath.Join(root, "simulator", "instances"), qemu, swtpm, environment, TCG})
	if err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, childFD := os.NewFile(uintptr(fds[0]), "parent"), os.NewFile(uintptr(fds[1]), "child")
	defer parent.Close()
	command := exec.Command(self, "test-owner")
	command.Env = append(environment, "NVKR_TEST_NATIVE_QEMU=1", "NVKR_TEST_NATIVE_QEMU_ROOT="+root, "NVKR_TEST_NATIVE_QEMU_REAL=1")
	command.ExtraFiles = []*os.File{nil, childFD}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childFD.Close()
	ownerJoined := false
	t.Cleanup(func() {
		if !ownerJoined {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
	})
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			ownerJoined = true
			t.Fatalf("real device exited before cancellation: %v\n%s", err, output.String())
		default:
		}
		if _, err := os.Stat(filepath.Join(adapter.Instances, "instance", "qemu.pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
				ownerJoined = true
			case <-time.After(6 * time.Second):
				t.Fatal("real device failed startup and could not join")
			}
			t.Fatal("real QEMU did not produce its PID file", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	pidBytes, err := os.ReadFile(filepath.Join(adapter.Instances, "instance", "qemu.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatal("real QEMU PID unavailable", err)
	}
	probe, probeCancel := context.WithTimeout(t.Context(), 5*time.Second)
	actualArgs, err := exec.CommandContext(probe, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	probeCancel()
	if err != nil || !strings.Contains(string(actualArgs), "-machine virt,accel=tcg -cpu max") {
		t.Fatalf("real child did not receive explicit TCG/max: %v %s", err, actualArgs)
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != command.Process.Pid {
		t.Fatal("real QEMU escaped its owner process group", pgid, err)
	}
	select {
	case err := <-done:
		ownerJoined = true
		t.Fatalf("real TPM launch failed: %v\n%s", err, output.String())
	case <-time.After(500 * time.Millisecond):
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		ownerJoined = true
	case <-time.After(6 * time.Second):
		t.Fatal("real device owner did not join after cancellation")
	}
	if err := adapter.Close(t.Context()); err != nil {
		t.Fatal(err, output.String())
	}
	state, err := os.ReadDir(filepath.Join(adapter.Instances, "instance", "nvkr-tpm"))
	if err != nil || len(state) == 0 {
		t.Fatal("real swtpm did not retain state", err)
	}
	t.Log("Real installed explicit TCG/max + swtpm launch and joined cancellation passed; machine remained paused without an OS disk. Guest 64K boot, TPM discovery and attestation remain unverified.")
}
