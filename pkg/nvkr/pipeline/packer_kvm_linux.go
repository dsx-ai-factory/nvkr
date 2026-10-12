// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"golang.org/x/sys/unix"
)

func observePackerKVMProjection(ctx context.Context, device *os.File, enabled bool) (*packerKVMProjection, error) {
	if !enabled {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "observing Docker KVM projection")
	}
	var stat unix.Stat_t
	if device == nil {
		return nil, e.New(e.CodeEngineCapability, "Docker KVM projection requires the held device")
	}
	if err := unix.Fstat(int(device.Fd()), &stat); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "observing held KVM device numbers")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return nil, e.New(e.CodeEngineCapability, "Docker KVM projection requires a character device")
	}
	boot, err := os.Open("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening Linux kernel boot identity")
	}
	defer boot.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(boot.Fd()), &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, e.New(e.CodeEngineCapability, "Linux kernel boot identity requires procfs: %v", err)
	}
	// The kernel owns this value. Treat it as bounded opaque text, not a UUID
	// grammar or a caller-supplied identity; the consumer compares the same value.
	raw, err := io.ReadAll(io.LimitReader(boot, 129))
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "reading Linux kernel boot identity")
	}
	bootID := strings.TrimSuffix(string(raw), "\n")
	if !safetext.Valid(bootID, 128, false) {
		return nil, e.New(e.CodeEngineCapability, "Linux kernel boot identity is empty or non-canonical")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "observing Docker KVM projection")
	}
	return &packerKVMProjection{device: device, bootID: bootID,
		deviceNumber: fmt.Sprintf("%x:%x", unix.Major(stat.Rdev), unix.Minor(stat.Rdev))}, nil
}

func validateEnabledPackerContainerKVM(runtime packerContainerRuntime, kvm *os.File) error {
	if runtime.Endpoint() != qualifiedRuntimeEndpoint || kvm == nil {
		return e.New(e.CodeEngineCapability, "Packer KVM resolution requires a held device on the local runtime")
	}
	info, err := kvm.Stat()
	if err != nil || info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice == 0 {
		if err == nil {
			err = errors.New("descriptor is not a character device")
		}
		return e.Wrap(e.CodeEngineCapability, err, "validating admitted KVM device")
	}
	procPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), kvm.Fd())
	target, err := os.Readlink(procPath)
	if err != nil || target != "/dev/kvm" {
		if err == nil {
			err = fmt.Errorf("held device resolves to %q", target)
		}
		return e.Wrap(e.CodeEngineCapability, err,
			"validating local Docker KVM device handoff")
	}
	// KVM_GET_API_VERSION (_IO(KVMIO, 0x00) in linux/kvm.h) returns the
	// version as the syscall result and requires a zero argument. IoctlGetInt
	// instead passes a pointer, which KVM rejects with EINVAL.
	version, err := unix.IoctlRetInt(int(kvm.Fd()), 0xAE00)
	if err != nil || version != 12 {
		if err == nil {
			err = fmt.Errorf("KVM API version is %d", version)
		}
		return e.Wrap(e.CodeEngineCapability, err, "revalidating admitted KVM capability")
	}
	return nil
}
