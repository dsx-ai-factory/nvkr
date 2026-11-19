// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"

	"golang.org/x/sys/unix"
)

func networkStatIdentity(st unix.Stat_t) networkFileID {
	return networkFileID{st.Dev, st.Ino, st.Mode & 0o7777, st.Uid, st.Gid}
}

func networkProbePort(udp bool, port int) (result error) {
	kind := unix.SOCK_STREAM
	if udp {
		kind = unix.SOCK_DGRAM
	}
	fd, err := unix.Socket(unix.AF_INET, kind|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unix.Close(fd)) }()
	if err = unix.SetsockoptInt(fd, unix.SOL_IP, unix.IP_FREEBIND, 1); err != nil {
		return err
	}
	if udp && port == 67 {
		if err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
			return err
		}
	}
	return unix.Bind(fd, &unix.SockaddrInet4{Port: port, Addr: [4]byte{172, 30, 0, 1}})
}
