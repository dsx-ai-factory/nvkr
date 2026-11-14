// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalTreeTraversalRejectsIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "host-secret"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "blobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotRegularTreeContext(t.Context(), root); err == nil {
		t.Fatal("descriptor-relative terminal traversal followed an intermediate symlink")
	}
}

func repeatHex(v string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += v
	}
	return out
}
