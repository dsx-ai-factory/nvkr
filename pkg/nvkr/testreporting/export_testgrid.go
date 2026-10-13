// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// ExportTestGrid materializes the verified local projection in one new directory.
// This action never writes a service, overwrites an existing attempt or marks the
// detached projection Published. A late commit failure names its partial state.
func (r *Report) ExportTestGrid(ctx context.Context, destination TestGridDestination, out string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if out == "" {
		return e.New(e.CodeUsage, "explicit local TestGrid projection output required")
	}
	projection, err := r.TestGrid(ctx, destination)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "resolving projection output")
	}
	parent, parentInfo, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding TestGrid projection parent")
	}
	defer parent.Close()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "allocating projection stage")
	}
	stageName := ".nvkr-testgrid-" + hex.EncodeToString(token[:])
	if err := unix.Mkdirat(int(parent.Fd()), stageName, 0o700); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "creating projection stage")
	}
	stage, stageInfo, err := securepath.OpenChild(parent, stageName, true)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding projection stage")
	}
	defer stage.Close()
	committed := false
	defer func() {
		if !committed {
			cleanupGridStage(stage, projection)
			_ = unix.Unlinkat(int(parent.Fd()), stageName, unix.AT_REMOVEDIR)
		}
	}()
	for _, member := range projection.Files {
		if err := checkContext(ctx); err != nil {
			return err
		}
		memberParent, leaf, err := securepath.CreateParent(stage, member.Name)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "creating projection member parent")
		}
		fd, err := unix.Openat(int(memberParent.Fd()), leaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			_ = memberParent.Close()
			return e.Wrap(e.CodeVerifyFailed, err, "creating projection member")
		}
		file := os.NewFile(uintptr(fd), leaf)
		_, writeErr := file.Write(member.Bytes)
		syncErr := file.Sync()
		closeErr := file.Close()
		parentSyncErr := memberParent.Sync()
		_ = memberParent.Close()
		for _, cause := range []error{writeErr, syncErr, closeErr, parentSyncErr} {
			if cause != nil {
				return e.Wrap(e.CodeUnavailable, cause, "persisting projection member")
			}
		}
	}
	if err := stage.Sync(); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "persisting projection stage")
	}
	if _, err := r.Index(ctx); err != nil {
		return err
	}
	current, currentInfo, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rechecking projection parent")
	}
	_ = current.Close()
	if !os.SameFile(parentInfo, currentInfo) {
		return failure("TestGrid projection parent was replaced")
	}
	named, namedInfo, err := securepath.OpenChild(parent, stageName, true)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rechecking projection stage")
	}
	_ = named.Close()
	if !os.SameFile(stageInfo, namedInfo) {
		return failure("TestGrid projection stage was replaced")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), stageName, int(parent.Fd()), filepath.Base(absolute)); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing projection without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "projection committed but parent sync failed; no rollback performed")
	}
	current, currentInfo, err = securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "projection committed but parent recheck failed; no rollback performed")
	}
	_ = current.Close()
	if !os.SameFile(parentInfo, currentInfo) {
		return failure("projection committed but requested parent was replaced; no rollback performed")
	}
	published, publishedInfo, err := securepath.OpenDirectory(absolute)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "projection committed but directory recheck failed; no rollback performed")
	}
	defer published.Close()
	if !os.SameFile(stageInfo, publishedInfo) {
		return failure("projection committed but output directory was replaced; no rollback performed")
	}
	if err := verifyGridProjection(published, projection); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "projection committed but exact local recheck failed; no rollback performed")
	}
	if _, err := r.Index(ctx); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "projection committed but receipt recheck failed; no rollback performed")
	}
	return nil
}
func cleanupGridStage(root *os.File, projection TestGridProjection) {
	_ = unix.Unlinkat(int(root.Fd()), "started.json", 0)
	_ = unix.Unlinkat(int(root.Fd()), "finished.json", 0)
	artifacts, _, err := securepath.OpenChild(root, "artifacts", true)
	if err == nil {
		for _, member := range projection.Files {
			if filepath.Dir(member.Name) == "artifacts" {
				_ = unix.Unlinkat(int(artifacts.Fd()), filepath.Base(member.Name), 0)
			}
		}
		_ = artifacts.Close()
		_ = unix.Unlinkat(int(root.Fd()), "artifacts", unix.AT_REMOVEDIR)
	}
}

func verifyGridProjection(root *os.File, projection TestGridProjection) error {
	entries, err := root.ReadDir(4)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) != 3 {
		return failure("projection root membership differs")
	}
	artifacts, _, err := securepath.OpenChild(root, "artifacts", true)
	if err != nil {
		return err
	}
	defer artifacts.Close()
	artifactEntries, err := artifacts.ReadDir(len(projection.Files))
	if err != nil && err != io.EOF {
		return err
	}
	if len(artifactEntries) != len(projection.Files)-2 {
		return failure("projection artifact membership differs")
	}
	for _, member := range projection.Files {
		parent := root
		leaf := member.Name
		if filepath.Dir(member.Name) == "artifacts" {
			parent = artifacts
			leaf = filepath.Base(member.Name)
		}
		file, _, err := securepath.OpenChild(parent, leaf, false)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, int64(len(member.Bytes))+1))
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(raw, member.Bytes) {
			return failure("local projection bytes differ from verified input")
		}
	}
	return nil
}
