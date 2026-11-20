// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// Ownership comes from the held runner workspace, never from sudo environment
// variables. Only transport outputs and sealed diagnostics are returned, after
// every execution owner has stopped. Runtime roots retain their private owner.
type diskOwnership struct{ uid, gid int }

func captureDiskOwnership(ctx context.Context, workspace *os.File) (diskOwnership, error) {
	if err := ctx.Err(); err != nil {
		return diskOwnership{}, e.Wrap(e.CodeTimeout, err, "disk workspace ownership canceled")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(workspace.Fd()), &stat); err != nil {
		return diskOwnership{}, e.Wrap(e.CodeEngineCapability, err, "inspect disk workspace owner")
	}
	return diskOwnership{int(stat.Uid), int(stat.Gid)}, nil
}

func (owner diskOwnership) finish(ctx context.Context, workspace *os.File, plan *Plan, stage githubStage, diagnostics string) error {
	if os.Geteuid() != 0 || owner.uid == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	paths := []string{githubInspectionLogPath(stage.DiskOutput)}
	if diagnostics != "" {
		paths = append(paths, diagnostics)
	}
	for _, node := range stage.Nodes {
		for _, id := range githubCarrierOutputs(plan, node, artifacts, nodeJobs) {
			paths = append(paths, artifacts[id].Path)
		}
	}
	for _, path := range uniqueSorted(paths) {
		if err := owner.returnPath(ctx, workspace, path); err != nil {
			return err
		}
	}
	return nil
}

func (owner diskOwnership) returnPath(ctx context.Context, workspace *os.File, path string) error {
	if !securepath.WorkspaceRelative(path) || filepath.Clean(path) != path {
		return e.New(e.CodeVerifyFailed, "unsafe disk output ownership path")
	}
	return owner.returnParts(ctx, workspace, strings.Split(path, string(filepath.Separator)))
}

func (owner diskOwnership) returnParts(ctx context.Context, parent *os.File, parts []string) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "disk output ownership canceled")
	}
	entry, err := securepath.InspectChild(parent, parts[0], parts[0])
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} // Failed execution may leave only diagnostics.
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "inspect disk output ownership path")
	}
	if entry.File == nil {
		return e.New(e.CodeVerifyFailed, "disk output ownership refuses symbolic links")
	}
	defer entry.File.Close()
	if len(parts) > 1 {
		if entry.Kind != "directory" {
			return e.New(e.CodeVerifyFailed, "disk output ownership parent is not a directory")
		}
		if err := owner.returnParts(ctx, entry.File, parts[1:]); err != nil {
			return err
		}
	} else if entry.Kind == "directory" {
		if err := owner.returnChildren(ctx, entry.File); err != nil {
			return err
		}
	}
	return owner.returnFile(ctx, entry.File)
}

func (owner diskOwnership) returnChildren(ctx context.Context, directory *os.File) error {
	for {
		entries, err := directory.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return e.Wrap(e.CodeVerifyFailed, err, "read disk output ownership directory")
		}
		for _, entry := range entries {
			if err := owner.returnParts(ctx, directory, []string{entry.Name()}); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (owner diskOwnership) returnFile(ctx context.Context, file *os.File) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "disk output ownership canceled")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "inspect held disk output owner")
	}
	if int(stat.Uid) == owner.uid && int(stat.Gid) == owner.gid {
		return nil
	}
	if stat.Uid != 0 || (stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink != 1) {
		return e.New(e.CodeVerifyFailed, "disk output ownership requires an unshared coordinator-owned file")
	}
	if err := file.Chown(owner.uid, owner.gid); err != nil {
		return e.Wrap(e.CodeInternal, err, "return held disk output to runner")
	}
	return nil
}
