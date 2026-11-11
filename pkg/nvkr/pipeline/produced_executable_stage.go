// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const producedToolContainerDirectory = "/nvkr/internal-tool"
const producedToolFilename = "tool"

// Staging is private to a single container operation. The caller keeps it until
// container cleanup has joined; no mutable caller pathname becomes a mount.
type stagedProducedExecutable struct {
	root      string
	directory os.FileInfo
	file      os.FileInfo
	tool      *ProducedExecutable
	binding   ProducedExecutableBinding
}

func stageProducedExecutable(ctx context.Context, tool *ProducedExecutable, workspace *os.File) (_ *stagedProducedExecutable, returnErr error) {
	binding, err := tool.BindingContext(ctx)
	if err != nil {
		return nil, err
	}
	if workspace == nil {
		return nil, e.New(e.CodeEngineCapability, "produced tool staging requires a held workspace")
	}
	parent, err := filepath.Abs(workspace.Name())
	if err != nil || strings.ContainsAny(parent, ",:\r\n") {
		return nil, e.New(e.CodeEngineCapability, "produced tool workspace cannot be represented as an exact bind mount")
	}
	heldInfo, err := workspace.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "checking produced tool workspace")
	}
	parentFile, parentInfo, err := securepath.OpenDirectory(parent)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening produced tool workspace")
	}
	if err := parentFile.Close(); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "closing produced tool workspace check")
	}
	if !os.SameFile(heldInfo, parentInfo) {
		return nil, e.New(e.CodeVerifyFailed, "produced tool workspace path changed")
	}
	root, err := os.MkdirTemp(parent, ".nvkr-produced-tool-")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating produced tool staging")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return nil, e.New(e.CodeVerifyFailed, "produced tool staging is not its created directory")
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, removeProducedToolStage(root, rootInfo))
		}
	}()
	s := tool.state
	if err := lockDiskInspection(ctx, &s.mu); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if s.file == nil {
		return nil, e.New(e.CodeVerifyFailed, "produced tool closed before staging")
	}
	output, err := os.OpenFile(filepath.Join(root, producedToolFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o555)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating staged produced tool")
	}
	written, writeErr := s.file.WriteToContext(ctx, output)
	modeErr := output.Chmod(0o555)
	info, statErr := output.Stat()
	if err := errors.Join(writeErr, modeErr, statErr, output.Sync(), output.Close()); err != nil {
		return nil, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "staging produced tool"), "produced tool staging")
	}
	if written != binding.Size {
		return nil, e.New(e.CodeVerifyFailed, "produced tool changed while staging")
	}
	if err := os.Chmod(root, 0o555); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "making tool staging read-only")
	}
	directory, err := os.Lstat(root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "checking tool staging directory")
	}
	currentParent, err := os.Lstat(parent)
	if err != nil || !currentParent.IsDir() || !os.SameFile(currentParent, heldInfo) || !os.SameFile(directory, rootInfo) {
		return nil, e.New(e.CodeVerifyFailed, "produced tool staging workspace changed")
	}
	return &stagedProducedExecutable{root: root, directory: directory, file: info, tool: tool, binding: binding}, nil
}

func (stage *stagedProducedExecutable) verify(ctx context.Context) error {
	if stage == nil || stage.root == "" {
		return e.New(e.CodeVerifyFailed, "produced tool staging is absent")
	}
	binding, err := stage.tool.BindingContext(ctx)
	if err != nil {
		return err
	}
	if binding != stage.binding {
		return e.New(e.CodeVerifyFailed, "staged tool differs from its producer receipt")
	}
	directory, err := os.Lstat(stage.root)
	if err != nil || !directory.IsDir() || !os.SameFile(directory, stage.directory) || directory.Mode().Perm() != 0o555 {
		return e.New(e.CodeVerifyFailed, "produced tool staging directory changed")
	}
	digest, size, info, err := hashRegularBeneathContext(ctx, stage.root, producedToolFilename)
	if err != nil || !os.SameFile(stage.file, info) || info.Mode().Perm() != 0o555 || digest != binding.Digest || size != binding.Size {
		return mapCancellation(ctx, e.New(e.CodeVerifyFailed, "staged produced tool changed"), "produced tool verification")
	}
	dir, current, err := securepath.OpenDirectory(stage.root)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening staged tool directory")
	}
	entries, readErr := dir.Readdirnames(2)
	closeErr := dir.Close()
	if !os.SameFile(current, stage.directory) || (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || len(entries) != 1 || entries[0] != producedToolFilename {
		return e.New(e.CodeVerifyFailed, "produced tool staging contains unexpected entries")
	}
	return nil
}

func (stage *stagedProducedExecutable) close() error {
	if stage == nil || stage.root == "" {
		return nil
	}
	return removeProducedToolStage(stage.root, stage.directory)
}

func removeProducedToolStage(root string, expected os.FileInfo) error {
	current, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(current, expected) {
		return e.New(e.CodeVerifyFailed, "produced tool staging path changed before cleanup")
	}
	return removePrivateTree(root)
}
