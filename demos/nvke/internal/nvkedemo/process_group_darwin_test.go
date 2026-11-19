// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package nvkedemo

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestDarwinKernelObservesOwnedProcessGroup(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	alive, err := groupAlive(cmd.Process.Pid)
	if err != nil || !alive {
		t.Fatalf("live group: alive=%v, error=%v", alive, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed process returned success")
	}
	alive, err = groupAlive(cmd.Process.Pid)
	if err != nil || alive {
		t.Fatalf("reaped group: alive=%v, error=%v", alive, err)
	}
	for _, pid := range []int{0, -1} {
		if _, err := groupAlive(pid); err == nil {
			t.Fatalf("invalid group %d accepted", pid)
		}
	}
}
