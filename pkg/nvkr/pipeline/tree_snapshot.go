// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
)

type terminalHeldTree struct {
	snapshot *ociclosure.Tree
	digest   string
}

func snapshotRegularTreeContext(ctx context.Context, root string) (*terminalHeldTree, error) {
	tree, err := ociclosure.Snapshot(ctx, root)
	if err != nil {
		return nil, err
	}
	return &terminalHeldTree{snapshot: tree, digest: tree.Digest()}, nil
}
func (tree *terminalHeldTree) readRegular(ctx context.Context, path string, limit int64) ([]byte, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	return tree.snapshot.ReadRegular(ctx, path, limit)
}
func (tree *terminalHeldTree) blobInventory(ctx context.Context) (map[string]int64, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	return tree.snapshot.BlobInventory(ctx)
}
