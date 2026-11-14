// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
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

func writeResultForTest(t *testing.T, dir, nodeID string, result ArtifactResult) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sanitizeName(nodeID)+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func repeatHex(v string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += v
	}
	return out
}
