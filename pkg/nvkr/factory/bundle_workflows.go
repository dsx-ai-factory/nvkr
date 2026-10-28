// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Only this owner's legacy sibling names participate in the finite inventory.
// They must be prior-manifest-owned for retirement; unowned leftovers fail closed.
// Other producer namespaces and operator-authored workflows remain independent.
func checkGeneratedWorkflowContents(ctx context.Context, root string, bundle *GeneratedBundle) error {
	const relative = ".github/workflows"
	directory, err := generatedDestination(root, relative)
	if err != nil {
		return err
	}
	before, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return e.New(e.CodePipelineDrift, "generated workflow directory is unsafe")
	}
	if err := validateGeneratedDirectoryChain(root, relative); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "generated workflow directory has unsafe parents")
	}
	opened, err := os.Open(directory)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "opening generated workflow directory")
	}
	defer func() { _ = opened.Close() }()
	info, err := opened.Stat()
	if err != nil || !os.SameFile(before, info) {
		return e.New(e.CodePipelineDrift, "generated workflow directory changed while opening")
	}
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "generated workflow inventory canceled")
		}
		entries, readErr := opened.ReadDir(128)
		for _, entry := range entries {
			seen++
			if seen > maxGeneratedDirEntries {
				return e.New(e.CodePipelineDrift, "workflow directory exceeds %d entries", maxGeneratedDirEntries)
			}
			member := relative + "/" + entry.Name()
			owner, reserved := legacyGitHubChildWorkflowOwner(member)
			if !reserved || owner != bundle.WorkflowPath {
				continue
			}
			if _, allowed := bundle.Files[member]; !allowed {
				return e.New(e.CodePipelineDrift, "unexpected generated workflow %s", member)
			}
			info, err := os.Lstat(filepath.Join(directory, entry.Name()))
			if err != nil || !info.Mode().IsRegular() {
				return e.New(e.CodePipelineDrift, "generated workflow %s is unsafe", member)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return e.Wrap(e.CodePipelineDrift, readErr, "listing generated workflow directory")
		}
	}
	after, err := os.Lstat(directory)
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
		return e.New(e.CodePipelineDrift, "generated workflow directory changed during inspection")
	}
	return nil
}
