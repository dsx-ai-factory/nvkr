// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestARCKVMAccessRunnerFault(t *testing.T) {
	timeout, err := exec.LookPath("timeout")
	if err != nil {
		t.Skip("timeout unavailable")
	}
	for _, name := range []string{"missing", "regular", "symlink", "accessible character device"} {
		t.Run(name, func(t *testing.T) {
			device := filepath.Join(t.TempDir(), "kvm")
			switch name {
			case "regular":
				if err := os.WriteFile(device, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("/dev/null", device); err != nil {
					t.Fatal(err)
				}
			case "accessible character device":
				device = "/dev/null"
			}
			script := strings.ReplaceAll(githubKVMAccessCheck().Run, "/dev/kvm", device)
			script = strings.ReplaceAll(script, "/usr/bin/timeout", timeout)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-c", script)
			command.Env = append(os.Environ(), "RUNNER_NAME=arc-arm64-kvm-test-runner")
			output, err := command.CombinedOutput()
			if name == "accessible character device" {
				// This is intentionally an access check, not KVM API admission.
				if err != nil || !strings.Contains(string(output), "KVM access ready on ARC pod arc-arm64-kvm-test-runner") {
					t.Fatalf("access check: %v: %s", err, output)
				}
			} else {
				requireRunnerCapabilityFault(t, err, output)
			}
		})
	}
}

func TestARCKVMAccessTimeoutAndBootstrap(t *testing.T) {
	for _, test := range []struct {
		name, sequence, bounds string
		success                bool
	}{
		{"permission prepared", "1 0 0", "5s\n15s\n5s\n", true},
		{"permission denied", "1 1", "5s\n15s\n", false},
		{"permission timeout", "1 124", "5s\n15s\n", false},
		{"final access timeout", "0 124", "5s\n5s\n", false},
		{"still inaccessible", "1 0 1", "5s\n15s\n5s\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			timeout := filepath.Join(dir, "timeout")
			// Never run sudo/chmod in this fixture; model their bounded result.
			stub := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$2\" >> \"$TEST_BOUNDS\"\nn=0\nif [ -f \"$TEST_STATE\" ]; then n=$(cat \"$TEST_STATE\"); fi\nn=$((n+1))\nprintf '%s' \"$n\" > \"$TEST_STATE\"\ni=0\nfor status in $TEST_SEQUENCE; do i=$((i+1)); if [ \"$i\" = \"$n\" ]; then exit \"$status\"; fi; done\nexit 99\n"
			if err := os.WriteFile(timeout, []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(githubKVMAccessCheck().Run, "/dev/kvm", "/dev/null")
			script = strings.ReplaceAll(script, "/usr/bin/timeout", timeout)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-c", script)
			command.Env = append(os.Environ(), "RUNNER_NAME=arc-arm64-kvm-test-runner",
				"TEST_BOUNDS="+filepath.Join(dir, "bounds"), "TEST_STATE="+filepath.Join(dir, "state"),
				"TEST_SEQUENCE="+test.sequence)
			output, err := command.CombinedOutput()
			if test.success {
				if err != nil {
					t.Fatalf("access check: %v: %s", err, output)
				}
			} else {
				requireRunnerCapabilityFault(t, err, output)
			}
			bounds, err := os.ReadFile(filepath.Join(dir, "bounds"))
			if err != nil {
				t.Fatal(err)
			}
			if string(bounds) != test.bounds {
				t.Fatalf("bounds %q, want %q", bounds, test.bounds)
			}
		})
	}
}

func requireRunnerCapabilityFault(t *testing.T, err error, output []byte) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 29 ||
		!strings.Contains(string(output), "NVKR-E-ENGINE-CAPABILITY: runner-capability fault on ARC pod arc-arm64-kvm-test-runner:") {
		t.Fatalf("access check: %v: %s", err, output)
	}
}
