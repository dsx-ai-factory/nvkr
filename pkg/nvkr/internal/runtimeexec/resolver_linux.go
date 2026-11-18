// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"fmt"
	"os"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func prepareResolverMountPoint(root *os.File) error {
	if err := prepareMountPoint(root, "etc"); err != nil {
		return err
	}
	etc, _, err := securepath.OpenChild(root, "etc", true)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding runtime resolver parent")
	}
	defer etc.Close()
	fd, err := unix.Openat(int(etc.Fd()), "resolv.conf", unix.O_CREAT|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o644)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "runtime resolver mount point must be a regular file")
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return e.New(e.CodeVerifyFailed, "runtime resolver mount point is not a private regular file")
	}
	return nil
}

func mountPrivateResolver(configuration string) error {
	if !validResolver(configuration) {
		return e.New(e.CodeUsage, "private resolver configuration is not canonical")
	}
	// /run is the new private tmpfs. The mounted root has already been protected;
	// this per-namespace file never changes package or production bytes.
	if err := os.WriteFile("/run/nvkr-resolv.conf", []byte(configuration), 0o444); err != nil {
		return entryError(err)
	}
	etc, _, err := securepath.OpenDirectory("/etc")
	if err != nil {
		return entryError(err)
	}
	defer etc.Close()
	target, info, err := securepath.OpenChild(etc, "resolv.conf", false)
	if err != nil {
		return entryError(err)
	}
	defer target.Close()
	if !info.Mode().IsRegular() {
		return e.New(e.CodeVerifyFailed, "private resolver mount target changed")
	}
	location := fmt.Sprintf("/proc/self/fd/%d", target.Fd())
	if err := namespaceMount("/run/nvkr-resolv.conf", location, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	// Reopen through the attached mount so attributes apply to the new bind.
	return namespaceMountSetattr(unix.AT_FDCWD, "/etc/resolv.conf", 0,
		unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV|unix.MOUNT_ATTR_NOEXEC)
}
