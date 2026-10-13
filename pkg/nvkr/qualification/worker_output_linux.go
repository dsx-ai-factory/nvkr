// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package qualification

import (
	"context"
	"fmt"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// The fixed output is an immutable transport commit, not durable smoke authority.
// A kernel-released directory lock serializes writers. Four bounded files are
// synced and protected before no-replace directory rename and parent fsync.
// An interrupted stage is refused on retry, never silently reused as success.
func commitSmokeWorkerFiles(ctx context.Context, workspace string, files smokeWorkerFiles, require func() error) error {
	if err := workerContext(ctx); err != nil {
		return err
	}
	if err := files.validate(); err != nil {
		return err
	}
	if require == nil {
		return e.New(e.CodeGateUnmet, "worker output requires live signing authority")
	}
	if err := require(); err != nil {
		return err
	}
	if workspace == "" {
		return e.New(e.CodeUsage, "worker output requires a workspace")
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private worker workspace")
	}
	fd, err := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening private worker workspace")
	}
	directory := os.NewFile(uintptr(fd), workspace)
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return e.New(e.CodeImmutableViolation, "worker workspace must be a private 0700 directory")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "locking worker evidence workspace")
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening held worker workspace")
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(smokeWorkerEvidenceDirectory); !os.IsNotExist(err) {
		return e.New(e.CodeImmutableViolation, "worker evidence destination already exists or cannot be inspected")
	}
	const stageName = ".worker-pending"
	if err := root.Mkdir(stageName, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating worker evidence stage; stale stages require inspection")
	}
	stage, err := root.OpenRoot(stageName)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening worker evidence stage")
	}
	defer func() { _ = stage.Close() }()
	stageDir, err := stage.Open(".")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening worker stage directory")
	}
	defer func() { _ = stageDir.Close() }()
	stageInfo, err := stageDir.Stat()
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "inspecting held worker evidence stage")
	}
	committed := false
	defer func() {
		if !committed {
			_ = stageDir.Chmod(0o700)
			for _, member := range files.members() {
				_ = stage.Remove(member.name)
			}
			if current, err := root.Lstat(stageName); err == nil && os.SameFile(stageInfo, current) {
				_ = root.Remove(stageName)
			}
		}
	}()
	for _, member := range files.members() {
		if err := workerContext(ctx); err != nil {
			return err
		}
		file, err := stage.OpenFile(member.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "creating immutable worker evidence")
		}
		_, writeErr := file.Write(member.raw)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return e.New(e.CodeImmutableViolation, "writing or syncing immutable worker evidence failed")
		}
	}
	if err := stageDir.Chmod(0o500); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "protecting worker evidence directory")
	}
	if err := stageDir.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing worker evidence directory")
	}
	if err := require(); err != nil {
		return err
	}
	if err := workerContext(ctx); err != nil {
		return err
	}
	current, err := os.Lstat(workspace)
	if err != nil || !os.SameFile(info, current) {
		return e.New(e.CodeImmutableViolation, "worker workspace path changed before commit")
	}
	currentStage, err := root.Lstat(stageName)
	if err != nil || !os.SameFile(stageInfo, currentStage) {
		return e.New(e.CodeImmutableViolation, "worker evidence stage changed before commit")
	}
	if err := unix.Renameat2(fd, stageName, fd, smokeWorkerEvidenceDirectory, unix.RENAME_NOREPLACE); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing immutable worker evidence")
	}
	committed = true
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing committed worker evidence")
	}
	if err := require(); err != nil {
		return err
	}
	return workerContext(ctx)
}
