// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// systemd-resolved owns its generated files as its service user. The root-owned
// fixed host link delegates only these two protected targets. This observation
// supplies DNS addresses, not source, runtime or publication authority.
func admitManagedResolver(ctx context.Context, expected unix.Stat_t) error {
	root, _, err := securepath.OpenDirectory("/")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "holding resolver host root")
	}
	defer root.Close()
	return admitManagedResolverAt(ctx, root, expected)
}

func admitManagedResolverAt(ctx context.Context, root *os.File, expected unix.Stat_t) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "managed resolver observation canceled")
	}
	deny := func() error {
		return e.New(e.CodeEngineCapability, "outbound resolver owner %d requires a protected root-delegated systemd-resolved target", expected.Uid)
	}
	protected := func(file *os.File, owner uint32) bool {
		var stat unix.Stat_t
		return unix.Fstat(int(file.Fd()), &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Uid == owner && stat.Mode&0o022 == 0
	}
	if !protected(root, 0) {
		return deny()
	}
	etc, _, err := securepath.OpenChild(root, "etc", true)
	if err != nil {
		return deny()
	}
	defer etc.Close()
	if !protected(etc, 0) {
		return deny()
	}
	var link unix.Stat_t
	if err := unix.Fstatat(int(etc.Fd()), "resolv.conf", &link, unix.AT_SYMLINK_NOFOLLOW); err != nil || link.Mode&unix.S_IFMT != unix.S_IFLNK || link.Uid != 0 {
		return deny()
	}
	var text [4096]byte
	count, err := unix.Readlinkat(int(etc.Fd()), "resolv.conf", text[:])
	if err != nil || count == len(text) {
		return deny()
	}
	target := string(text[:count])
	if !path.IsAbs(target) {
		target = path.Join("/etc", target)
	}
	if target != "/run/systemd/resolve/resolv.conf" && target != "/run/systemd/resolve/stub-resolv.conf" {
		return deny()
	}
	parent := root
	for _, name := range []string{"run", "systemd", "resolve"} {
		child, _, err := securepath.OpenChild(parent, name, true)
		if err != nil {
			return deny()
		}
		defer child.Close()
		uid := uint32(0)
		if name == "resolve" {
			uid = expected.Uid
		}
		if !protected(child, uid) && !(name == "resolve" && protected(child, 0)) {
			return deny()
		}
		parent = child
	}
	file, _, err := securepath.OpenChild(parent, path.Base(target), false)
	if err != nil {
		return deny()
	}
	defer file.Close()
	var actual unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &actual); err != nil || actual.Dev != expected.Dev || actual.Ino != expected.Ino ||
		actual.Mode&unix.S_IFMT != unix.S_IFREG || actual.Uid != expected.Uid || actual.Mode&0o022 != 0 || actual.Size > 64<<10 {
		return deny()
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "managed resolver observation canceled")
	}
	return nil
}

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
