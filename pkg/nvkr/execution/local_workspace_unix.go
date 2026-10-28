// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package execution

import (
	"os"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const bytesPerMiB = uint64(1024 * 1024)

type localFDDuplicator func(uintptr) (int, error)
type localStatfs func(int, *unix.Statfs_t) error

func holdLocalFD(fd int, name string) (*os.File, error) {
	if fd < 0 {
		return nil, e.New(e.CodeEngineCapability, "%s capability is closed", name)
	}
	held, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "duplicating held %s capability", name)
	}
	file := os.NewFile(uintptr(held), name)
	if file == nil {
		_ = unix.Close(held)
		return nil, e.New(e.CodeInternal, "constructing held %s capability", name)
	}
	return file, nil
}

func duplicateWorkspaceDirectory(workspace *os.File, duplicate localFDDuplicator) (int, error) {
	if workspace == nil {
		return -1, e.New(e.CodeUsage, "duplicating local workspace requires a held directory")
	}
	if duplicate == nil {
		return -1, e.New(e.CodeInternal, "local workspace descriptor duplicator is absent")
	}
	raw, err := workspace.SyscallConn()
	if err != nil {
		return -1, e.Wrap(e.CodeEngineCapability, err, "accessing the held local workspace descriptor")
	}
	duplicated := -1
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) {
		duplicated, duplicateErr = duplicate(fd)
	}); err != nil {
		return -1, e.Wrap(e.CodeEngineCapability, err,
			"holding the local workspace descriptor while duplicating it")
	}
	if duplicateErr != nil {
		return -1, e.Wrap(e.CodeEngineCapability, duplicateErr,
			"duplicating the held local workspace descriptor")
	}
	if duplicated < 0 {
		return -1, e.New(e.CodeInternal, "local workspace descriptor duplicator returned an invalid descriptor")
	}
	return duplicated, nil
}

func availableScratchMiB(fd int) (uint64, error) {
	return availableScratchMiBWith(fd, unix.Fstatfs)
}

func availableScratchMiBWith(fd int, statfs localStatfs) (uint64, error) {
	if fd < 0 {
		return 0, e.New(e.CodeInternal, "local scratch directory handle is closed")
	}
	if statfs == nil {
		return 0, e.New(e.CodeInternal, "local scratch filesystem observer is absent")
	}
	var stat unix.Statfs_t
	if err := statfs(fd, &stat); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local scratch capacity")
	}
	facts := securepath.FilesystemSpaceFromStat(stat)
	if facts.ReadOnly() {
		return 0, e.New(e.CodeEngineCapability, "local scratch filesystem is read-only")
	}
	available, err := facts.AvailableBytes()
	if err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local scratch capacity")
	}
	// General VM ephemeral scratch may be memory-backed. Large consumer QCOW2
	// snapshots have a separate policy that rejects detected memory filesystems.
	return available / bytesPerMiB, nil
}
