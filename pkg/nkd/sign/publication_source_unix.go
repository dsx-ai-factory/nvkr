// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sign

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openPublicationRegular converts an untrusted publication pathname into a
// held regular-file capability. O_NONBLOCK makes FIFO/device substitution fail
// at admission instead of hanging recovery; O_NOFOLLOW rejects a final
// symlink. The path and held descriptor must still identify the same inode.
func openPublicationRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening %q produced no file", path)
	}
	held, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !held.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(held, current) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%q is not a stable no-follow regular file", path)
	}
	return file, held, nil
}
