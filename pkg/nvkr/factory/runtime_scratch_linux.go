// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeexec"
	"golang.org/x/sys/unix"
)

func newRuntimeScratch(ctx context.Context, storageMiB uint64) (_ *runtimeScratch, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime scratch requires context")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, e.New(e.CodeEngineCapability, "runtime scratch requires a bounded disk owner")
	}
	if storageMiB == 0 || storageMiB > ^uint64(0)/(1<<20) {
		return nil, e.New(e.CodeEngineCapability, "runtime scratch requires its admitted storage budget")
	}
	// Probe both privilege boundaries before creating any filesystem state.
	for _, admit := range []func(context.Context) (*runtimeexec.Launcher, error){runtimeexec.AdmitOutbound, runtimeexec.AdmitAssembly} {
		launcher, err := admit(ctx)
		if err != nil {
			return nil, err
		}
		if err := launcher.Close(); err != nil {
			return nil, err
		}
	}
	parent, _, err := securepath.OpenDirectory("/opt")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening fixed native scratch parent")
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, parent.Close())
		}
	}()
	var owner unix.Stat_t
	var filesystem unix.Statfs_t
	if err := unix.Fstat(int(parent.Fd()), &owner); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "observing native scratch parent")
	}
	if owner.Uid != 0 || owner.Mode&0o022 != 0 || owner.Mode&0o001 == 0 {
		return nil, e.New(e.CodeEngineCapability, "native scratch parent must be root-owned, searchable and protected")
	}
	if err := unix.Fstatfs(int(parent.Fd()), &filesystem); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "observing native scratch filesystem")
	}
	if filesystem.Flags&(unix.ST_NOEXEC|unix.ST_RDONLY) != 0 {
		return nil, e.New(e.CodeEngineCapability, "native scratch filesystem must already permit execution and writes")
	}
	available, err := securepath.AvailableDiskBackedBytes(ctx, parent)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "observing native scratch capacity")
	}
	if available < storageMiB*(1<<20) {
		return nil, e.New(e.CodeEngineCapability, "native scratch has %d MiB available; admitted disk group requires %d MiB", available/(1<<20), storageMiB)
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "native scratch creation canceled")
	}
	path, err := os.MkdirTemp(fmt.Sprintf("/proc/self/fd/%d", parent.Fd()), "nvkr-runtime-owner-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private runtime scratch")
	}
	name := filepath.Base(path)
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, removeRuntimeScratch(parent, name))
		}
	}()
	directory, _, err := securepath.OpenChild(parent, name, true)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "holding private runtime scratch")
	}
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, directory.Close())
		}
	}()
	// Mapped assembly IDs must traverse this parent, but cannot list or write it.
	// Assembly and consumer children each retain their private 0700 directories.
	if err := directory.Chmod(0o711); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "admitting mapped assembly traversal")
	}
	accepted = true
	return &runtimeScratch{parent: parent, directory: directory, location: filepath.Join("/opt", name)}, nil
}

func removeRuntimeScratch(parent *os.File, name string) error {
	return os.RemoveAll(fmt.Sprintf("/proc/self/fd/%d/%s", parent.Fd(), name))
}
