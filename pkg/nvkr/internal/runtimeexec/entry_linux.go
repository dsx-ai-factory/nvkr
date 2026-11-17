// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// Entry runs only in the dedicated child process. Its caller must exit the
// process after any return: failure can leave partially applied restrictions.
func Entry() error {
	runtime.LockOSThread()
	if err := platformAdmission(); err != nil {
		return err
	}
	if os.Getpid() != 1 {
		return e.New(e.CodeEngineCapability, "runtime bootstrap is not the private PID namespace owner")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return entryError(err)
	}
	request := os.NewFile(4, "runtime-request")
	defer request.Close()
	seals, err := unix.FcntlInt(request.Fd(), unix.F_GET_SEALS, 0)
	const requiredSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if err != nil || seals&requiredSeals != requiredSeals {
		return e.New(e.CodeVerifyFailed, "runtime request is not immutable")
	}
	raw, err := io.ReadAll(io.LimitReader(request, maxRequestBytes+1))
	if err != nil || len(raw) > maxRequestBytes {
		return e.New(e.CodeVerifyFailed, "runtime request exceeds its bound")
	}
	var record requestRecord
	if err := canonical.UnmarshalExact(raw, &record); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding runtime namespace request")
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	if record.Mode == "assemble" || record.Mode == "probe-assembly" {
		if err := admitAssemblyIDs(); err != nil {
			return err
		}
	}
	if err := namespaceMount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "privatizing namespace mount propagation")
	}
	if err := loopbackUp(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "enabling private namespace loopback")
	}
	if record.Mode == "probe" || record.Mode == "probe-assembly" {
		return probeFilesystemAndPrivileges(record)
	}
	if err := enterRoot(record); err != nil {
		return err
	}
	if err := restrictPayload(record); err != nil {
		return err
	}
	if err := unix.Chdir(record.Workdir); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "chdir(path=%q) before namespace payload execution", record.Workdir)
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC|unix.CLOSE_RANGE_UNSHARE); err != nil {
		return entryError(err)
	}
	if err := unix.Exec(record.Command[0], record.Command, record.Environment); err != nil {
		var filesystem unix.Statfs_t
		statErr := unix.Statfs("/", &filesystem)
		return e.Wrap(e.CodeEngineCapability, err, "execve(path=%q), root statfs flags=%#x (statfs error=%v)", record.Command[0], filesystem.Flags, statErr)
	}
	return nil
}

func probeFilesystemAndPrivileges(record requestRecord) error {
	// A detached tmpfs tests the mount and device policy without creating a host
	// pathname or producer output. It disappears when these descriptors close.
	contextFD, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "probing detached tmpfs support")
	}
	defer func() { _ = unix.Close(contextFD) }()
	if err := unix.FsconfigSetString(contextFD, "size", "1m"); err != nil {
		return entryError(err)
	}
	if err := unix.FsconfigCreate(contextFD); err != nil {
		return entryError(err)
	}
	mountFD, err := unix.Fsmount(contextFD, unix.FSMOUNT_CLOEXEC, unix.MOUNT_ATTR_NOSUID)
	if err != nil {
		return entryError(err)
	}
	defer func() { _ = unix.Close(mountFD) }()
	if record.Mode == "probe" {
		if err := unix.Mknodat(mountFD, "null", unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "probing private device creation")
		}
	}
	if err := unix.MountSetattr(mountFD, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE,
		&unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "probing recursive read-only mount attributes")
	}
	return restrictPayload(record)
}

func admitAssemblyIDs() error {
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, err := os.ReadFile("/proc/self/" + name)
		if err != nil {
			return entryError(err)
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 3 || fields[0] != "0" || fields[1] != strconv.Itoa(AssemblyHostID) || fields[2] != strconv.Itoa(AssemblyIDCount) {
			return e.New(e.CodeEngineCapability, "assembly requires its private user namespace without host root in the ID mapping")
		}
	}
	return nil
}

func restrictPayload(record requestRecord) error {
	if record.Mode != "assemble" && record.Mode != "probe-assembly" {
		return dropPrivileges(record)
	}
	// Package installation needs chown and nested mounts. Those capabilities
	// belong exclusively to the verified child user namespace; host root is
	// unmapped. No set-ID executable may obtain additional privilege.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return entryError(err)
	}
	return entryError(unix.Setgroups([]int{}))
}

func enterRoot(record requestRecord) error {
	root := os.NewFile(uintptr(record.RootFD), "runtime-root")
	defer root.Close()
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return e.New(e.CodeVerifyFailed, "runtime root descriptor is not a directory")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(record.RootFD, &stat); err != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 {
		return e.New(e.CodeVerifyFailed, "runtime staging root is not protected by the privileged owner")
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", record.RootFD))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return e.New(e.CodeVerifyFailed, "runtime staging root has no stable private location")
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(rootPath))
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "opening runtime staging parent %q in the child namespace", filepath.Dir(rootPath))
	}
	err = unix.Fstat(int(parent.Fd()), &stat)
	_ = parent.Close()
	if err != nil || stat.Uid != 0 || stat.Mode&0o077 != 0 {
		return e.New(e.CodeVerifyFailed, "runtime staging parent is not private to the privileged owner")
	}
	for _, name := range []string{"dev", "proc", "sys", "tmp", "run"} {
		if err := prepareMountPoint(root, name); err != nil {
			return err
		}
	}
	for _, projection := range record.Projections {
		if err := prepareMountPoint(root, strings.TrimPrefix(projection.Target, "/")); err != nil {
			return err
		}
	}
	// Inherited descriptors retain their parent's mount object. Both bind
	// endpoints must belong to this namespace. Re-resolve the held directory,
	// compare its inode, then use only the child-namespace descriptor.
	current, err := childNamespaceDirectory(root)
	if err != nil {
		return entryError(err)
	}
	defer current.Close()
	currentPath := fmt.Sprintf("/proc/self/fd/%d", current.Fd())
	if err := namespaceMount(currentPath, currentPath, "", unix.MS_BIND, ""); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "attaching runtime root mount in child namespace")
	}
	// Reopen the attached mount through its private location, then compare the
	// inode with the held input before using that mount as the namespace root.
	attached, attachedInfo, err := securepath.OpenDirectory(rootPath)
	if err != nil {
		return entryError(err)
	}
	defer attached.Close()
	if !os.SameFile(info, attachedInfo) {
		return e.New(e.CodeVerifyFailed, "runtime staging root changed during namespace entry")
	}
	// All later mount targets stay anchored to the held attached root. A rename
	// of an outer workspace directory cannot redirect a privileged mount.
	rootPath = fmt.Sprintf("/proc/self/fd/%d", attached.Fd())
	// Only add restrictions: a user namespace inherits locked mount flags from
	// its more privileged parent. Legacy remount flags could implicitly clear
	// those attributes (including atime), which the kernel correctly refuses.
	if err := namespaceMountSetattr(int(attached.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE,
		unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "protecting attached runtime root")
	}
	for _, projection := range record.Projections {
		target := filepath.Join(rootPath, strings.TrimPrefix(projection.Target, "/"))
		inherited := os.NewFile(uintptr(projection.FD), "runtime-projection")
		current, err := childNamespaceDirectory(inherited)
		_ = inherited.Close()
		if err != nil {
			return err
		}
		defer current.Close()
		source := fmt.Sprintf("/proc/self/fd/%d", current.Fd())
		if err := namespaceMount(source, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return entryError(err)
		}
		attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV)
		if !projection.Writable {
			attributes |= unix.MOUNT_ATTR_RDONLY
		}
		if err := namespaceMountSetattr(unix.AT_FDCWD, target, unix.AT_RECURSIVE, attributes); err != nil {
			return err
		}
	}
	for _, name := range []string{"tmp", "run", "dev", "sys"} {
		flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
		options := "size=64m,mode=0755"
		if name == "tmp" {
			options = "size=256m,mode=1777"
		}
		if name == "dev" {
			flags = unix.MS_NOSUID | unix.MS_NOEXEC
			options = "size=1m,mode=0755"
		}
		if err := namespaceMount("tmpfs", filepath.Join(rootPath, name), "tmpfs", flags, options); err != nil {
			return entryError(err)
		}
	}
	if err := privateDevices(rootPath, record.KVMFD, record.Mode == "assemble"); err != nil {
		return err
	}
	if err := namespaceMount("proc", filepath.Join(rootPath, "proc"), "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		var inherited unix.Statfs_t
		statErr := unix.Statfs("/proc", &inherited)
		pidNamespace, pidErr := os.Readlink("/proc/self/ns/pid")
		userNamespace, userErr := os.Readlink("/proc/self/ns/user")
		return e.Wrap(e.CodeEngineCapability, err,
			"mounting private proc for pid=%d, mode=%q, pidns=%q (error=%v), userns=%q (error=%v), inherited proc flags=%#x (statfs error=%v)",
			os.Getpid(), record.Mode, pidNamespace, pidErr, userNamespace, userErr, inherited.Flags, statErr)
	}
	if err := namespaceMountSetattr(unix.AT_FDCWD, filepath.Join(rootPath, "sys"), unix.AT_RECURSIVE,
		unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV); err != nil {
		return entryError(err)
	}
	if err := unix.Sethostname([]byte("nvkr-runtime")); err != nil {
		return entryError(err)
	}
	if err := unix.Fchdir(int(attached.Fd())); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "fchdir(fd=%d) before pivot_root", attached.Fd())
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "pivot_root(new_root=%q, put_old=%q), root fd=%d", ".", ".", attached.Fd())
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "umount2(target=%q, flags=%#x) after pivot_root", ".", unix.MNT_DETACH)
	}
	if err := unix.Chdir("/"); err != nil {
		return entryError(err)
	}
	return nil
}

func childNamespaceDirectory(inherited *os.File) (*os.File, error) {
	info, err := inherited.Stat()
	if err != nil || !info.IsDir() {
		return nil, e.New(e.CodeVerifyFailed, "runtime mount input is not a held directory")
	}
	location, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", inherited.Fd()))
	if err != nil || !filepath.IsAbs(location) || strings.HasSuffix(location, " (deleted)") {
		return nil, e.New(e.CodeVerifyFailed, "runtime mount input has no child-namespace location")
	}
	current, observed, err := securepath.OpenDirectory(location)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "resolving runtime mount input in child namespace")
	}
	if !os.SameFile(info, observed) {
		_ = current.Close()
		return nil, e.New(e.CodeVerifyFailed, "runtime mount input changed while entering child namespace")
	}
	return current, nil
}

func namespaceMount(source, target, filesystem string, flags uintptr, data string) error {
	if err := unix.Mount(source, target, filesystem, flags, data); err != nil {
		var errno unix.Errno
		_ = errors.As(err, &errno)
		return e.Wrap(e.CodeEngineCapability, err, "mount(source=%q, target=%q, fstype=%q, flags=%#x, data=%q), errno=%d", source, target, filesystem, flags, data, errno)
	}
	return nil
}

func namespaceMountSetattr(fd int, location string, flags uint, attributes uint64) error {
	if err := unix.MountSetattr(fd, location, flags, &unix.MountAttr{Attr_set: attributes}); err != nil {
		var errno unix.Errno
		_ = errors.As(err, &errno)
		return e.Wrap(e.CodeEngineCapability, err,
			"mount_setattr(dirfd=%d, path=%q, flags=%#x, attr_set=%#x, attr_clr=0), errno=%d", fd, location, flags, attributes, errno)
	}
	return nil
}

func prepareMountPoint(root *os.File, name string) error {
	if err := unix.Mkdirat(int(root.Fd()), name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return entryError(err)
	}
	directory, _, err := securepath.OpenChild(root, name, true)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "runtime mount point must be a real directory")
	}
	return directory.Close()
}

func privateDevices(root string, kvmFD int, assembly bool) error {
	for _, device := range []struct {
		name         string
		major, minor uint32
	}{
		{"null", 1, 3}, {"zero", 1, 5}, {"random", 1, 8}, {"urandom", 1, 9},
	} {
		if assembly {
			if err := bindAssemblyDevice(root, device.name, device.major, device.minor); err != nil {
				return err
			}
			continue
		}
		if err := unix.Mknod(filepath.Join(root, "dev", device.name), unix.S_IFCHR|0o666, int(unix.Mkdev(device.major, device.minor))); err != nil {
			return entryError(err)
		}
		if err := os.Chmod(filepath.Join(root, "dev", device.name), 0o666); err != nil {
			return entryError(err)
		}
	}
	if kvmFD != 0 {
		var info unix.Stat_t
		if err := unix.Fstat(kvmFD, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(info.Rdev) != 10 || unix.Minor(info.Rdev) != 232 {
			return e.New(e.CodeVerifyFailed, "runtime KVM descriptor changed its admitted device")
		}
		api, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(kvmFD), 0xae00, 0)
		if errno != 0 || api != 12 {
			return e.New(e.CodeEngineCapability, "runtime KVM API is unavailable")
		}
		device := filepath.Join(root, "dev/kvm")
		if err := unix.Mknod(device, unix.S_IFCHR|0o600, int(info.Rdev)); err != nil {
			return entryError(err)
		}
		if err := os.Chown(device, 65534, 65534); err != nil {
			return entryError(err)
		}
	}
	if err := os.Symlink("/proc/self/fd", filepath.Join(root, "dev/fd")); err != nil {
		return entryError(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dev/shm"), 0o755); err != nil {
		return entryError(err)
	}
	return namespaceMount("tmpfs", filepath.Join(root, "dev/shm"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=64m,mode=1777")
}

// User namespaces cannot create usable host device nodes. Bind only these four
// fixed, inode-checked character devices into the private /dev; never expose the
// host device directory or accept an authored device pathname.
func bindAssemblyDevice(root, name string, major, minor uint32) error {
	fd, err := unix.Open("/dev/"+name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return entryError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	var observed unix.Stat_t
	if err := unix.Fstat(fd, &observed); err != nil || observed.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(observed.Rdev) != major || unix.Minor(observed.Rdev) != minor {
		return e.New(e.CodeEngineCapability, "assembly requires the fixed %s character device", name)
	}
	target := filepath.Join(root, "dev", name)
	placeholder, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return entryError(err)
	}
	if err := placeholder.Close(); err != nil {
		return entryError(err)
	}
	return namespaceMount(fmt.Sprintf("/proc/self/fd/%d", fd), target, "", unix.MS_BIND, "")
}

func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return entryError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return entryError(err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return entryError(err)
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	return entryError(unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request))
}

func dropPrivileges(record requestRecord) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return entryError(err)
	}
	// Lock NOROOT and NO_SETUID_FIXUP before changing IDs; clear every capability
	// explicitly. No set-ID file or subsequent exec can restore these privileges.
	if err := unix.Prctl(unix.PR_SET_SECUREBITS, 0x1|0x2|0x4|0x8, 0, 0, 0); err != nil {
		return entryError(err)
	}
	for capability := uintptr(0); capability < 64; capability++ {
		err := unix.Prctl(unix.PR_CAPBSET_DROP, capability, 0, 0, 0)
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil {
			return entryError(err)
		}
	}
	if err := unix.Setgroups([]int{}); err != nil {
		return entryError(err)
	}
	if err := unix.Setresgid(record.GID, record.GID, record.GID); err != nil {
		return entryError(err)
	}
	if err := unix.Setresuid(record.UID, record.UID, record.UID); err != nil {
		return entryError(err)
	}
	capabilities := [2]unix.CapUserData{}
	return entryError(unix.Capset(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &capabilities[0]))
}

func entryError(err error) error {
	if err == nil {
		return nil
	}
	return e.Wrap(e.CodeEngineCapability, err, "applying native runtime namespace boundary")
}
