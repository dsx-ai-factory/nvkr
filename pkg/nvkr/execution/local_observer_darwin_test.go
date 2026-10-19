// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestDarwinMemoryCounters(t *testing.T) {
	valid := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 64.\nPages inactive: 128.\nPages speculative: 64.\nPages purgeable: 128.\n"
	for _, test := range []struct {
		name, raw string
		pageSize  uint64
		want      uint64
		code      e.Code
	}{
		{name: "native ARM pages", raw: valid, pageSize: 16384, want: 4},
		{name: "smaller pages", raw: valid, pageSize: 4096, want: 1},
		{name: "missing queue", raw: "Pages free: 64.\nPages inactive: 128.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "duplicate", raw: valid + "Pages free: 1.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "negative", raw: "Pages free: -1.\nPages inactive: 128.\nPages speculative: 64.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "counter overflow", raw: "Pages free: 18446744073709551615.\nPages inactive: 1.\nPages speculative: 0.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "byte overflow", raw: "Pages free: 18446744073709551615.\nPages inactive: 0.\nPages speculative: 0.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "malformed", raw: "Pages free: 64x.\nPages inactive: 128.\nPages speculative: 64.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "missing terminator", raw: "Pages free: 64\nPages inactive: 128.\nPages speculative: 64.\n", pageSize: 16384, code: e.CodeEngineCapability},
		{name: "page size absent", raw: valid, code: e.CodeEngineCapability},
		{name: "invalid page size", raw: valid, pageSize: 3, code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := darwinMemoryMiB([]byte(test.raw), test.pageSize)
			if errorCode(err) != test.code || got != test.want {
				t.Fatalf("memory=%d error=%v; want %d/%s", got, err, test.want, test.code)
			}
		})
	}
}

func TestDarwinObservationRetainsWorkspaceAndJoins(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "workspace")
	moved := filepath.Join(root, "moved")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := (systemLocalObserver{}).Open(t.Context(), directory)
	if err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	defer lease.Close()
	before, err := directory.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	observed, err := lease.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if observed.Platform != HostPlatformDarwinARM64 || observed.Resources.CPUMillis == 0 || observed.Resources.MemoryMiB == 0 || observed.Resources.EphemeralStorageMiB == 0 {
		t.Fatalf("invalid live native observation: %+v", observed)
	}
	t.Logf("native host=%s cpu=%dm memory=%dMiB scratch=%dMiB capabilities=%v", observed.Platform, observed.Resources.CPUMillis, observed.Resources.MemoryMiB, observed.Resources.EphemeralStorageMiB, observed.Capabilities)
	capability := lease.(localVMCapabilityLease)
	held, err := capability.holdWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	after, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("workspace was reopened through a substituted pathname")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.holdKVM(); errorCode(err) != e.CodeEngineCapability {
		t.Fatalf("Darwin exposed KVM: %v", err)
	}
	if _, err := lease.Observe(t.Context()); errorCode(err) != e.CodeInternal {
		t.Fatalf("observation reused: %v", err)
	}
	if _, err := capability.reobserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.holdWorkspace(); errorCode(err) != e.CodeEngineCapability {
		t.Fatalf("closed capability borrowed: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinVMAdmissionUsesNativeBinding(t *testing.T) {
	requirements := &Requirements{Resources: &ResourceRequest{CPUMillis: 1000, MemoryMiB: 1, EphemeralStorageMiB: 1}, AllowedHostPlatforms: []HostPlatform{HostPlatformDarwinARM64}, Capabilities: []CapabilityRequirement{{Name: CapabilityHVF, Policy: CapabilityPreferred}}, TimeoutSeconds: 5}
	supported, err := unix.SysctlUint32("kern.hv_support")
	if err != nil {
		t.Fatal(err)
	}
	class := Class{Name: "local-mac", RunsOn: "local-mac", HostPlatform: HostPlatformDarwinARM64, Resources: ResourceCapacity{CPUMillis: 1000, MemoryMiB: 1, EphemeralStorageMiB: 1}, MaxTimeoutSeconds: 5}
	if supported == 1 {
		class.Capabilities = []CapabilityName{CapabilityHVF}
	}
	binding, err := Match(requirements, []Class{class}, MatchOptions{RequiredHostPlatform: HostPlatformDarwinARM64})
	if err != nil {
		t.Fatal(err)
	}
	request := LocalVMRequest{Subject: "native-test/node", Requirements: requirements, Binding: binding}
	admission, err := AdmitLocalVM(t.Context(), request, openLocalWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	called := false
	err = admission.Execute(t.Context(), request, func(ctx context.Context, runtime *LocalVMRuntime) error {
		called = true
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			return fmt.Errorf("authored deadline absent")
		}
		workspace, err := runtime.HoldWorkspace()
		if err != nil {
			return err
		}
		if _, err := workspace.Stat(); err != nil {
			return err
		}
		return workspace.Close()
	})
	if err != nil || !called {
		t.Fatalf("native admission callback=%t error=%v", called, err)
	}
	if err := admission.Execute(t.Context(), request, func(context.Context, *LocalVMRuntime) error { return nil }); errorCode(err) != e.CodeEngineCapability {
		t.Fatalf("admission reused: %v", err)
	}
}

func TestDarwinObservationRejectsCanceledAndNonDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if lease, err := (systemLocalObserver{}).Open(ctx, openLocalWorkspace(t)); lease != nil || errorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled open=%v/%v", lease, err)
	}
	file, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if lease, err := (systemLocalObserver{}).Open(t.Context(), file); lease != nil || errorCode(err) != e.CodeEngineCapability {
		t.Fatalf("regular file accepted=%v/%v", lease, err)
	}
}
