// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nvkedemo

import (
	"errors"

	"golang.org/x/sys/unix"
)

var errNetworkPortPlatform = errors.New("network port admission requires Linux")

func networkStatIdentity(st unix.Stat_t) networkFileID {
	return networkFileID{uint64(st.Dev), st.Ino, uint32(st.Mode) & 0o7777, st.Uid, st.Gid}
}

func networkProbePort(_ bool, _ int) error {
	return errNetworkPortPlatform
}
