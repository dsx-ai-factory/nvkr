// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package transportarchive

import "golang.org/x/sys/unix"

func publishTransportNoReplace(stage, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

// PublishAtNoReplace atomically renames an entry between held directories and
// rejects an existing destination. The caller owns directory synchronization.
func PublishAtNoReplace(sourceDirectory int, source string,
	destinationDirectory int, destination string,
) error {
	return unix.Renameat2(sourceDirectory, source, destinationDirectory, destination, unix.RENAME_NOREPLACE)
}
