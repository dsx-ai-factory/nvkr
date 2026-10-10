// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectedArtifactCopyPreservesBytesAndMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			payload := bytes.Repeat([]byte("artifact bytes\n"), 10000)
			if err := os.WriteFile(filepath.Join(root, "artifact"), payload, mode); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "collected", "artifact")
			if err := copyCollectedArtifactContext(t.Context(), root, "artifact", destination); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("collection changed bytes: %v", err)
			}
			info, err := os.Stat(destination)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("collection changed mode: %v, %v", info, err)
			}
			entries, err := os.ReadDir(filepath.Dir(destination))
			if err != nil || len(entries) != 1 || entries[0].Name() != "artifact" {
				t.Fatalf("collection leaked staging: %v, %v", entries, err)
			}
		})
	}
}

func TestCollectedArtifactRejectsUnsafeSourcesBeforeOutput(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "symlink", "escaping path", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			relative := "artifact"
			switch kind {
			case "directory":
				if err := os.Mkdir(filepath.Join(root, relative), 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(outside, filepath.Join(root, relative)); err != nil {
					t.Fatal(err)
				}
			case "escaping path":
				relative = "../outside"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			destination := filepath.Join(t.TempDir(), "collected", "artifact")
			if err := copyCollectedArtifactContext(ctx, root, relative, destination); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if _, err := os.Lstat(filepath.Dir(destination)); !os.IsNotExist(err) {
				t.Fatalf("rejected source created output: %v", err)
			}
		})
	}
}

func TestCollectedArtifactRenameFailureRemovesStaging(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "artifact"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "occupied")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyCollectedArtifactContext(t.Context(), root, "artifact", destination); err == nil {
		t.Fatal("replaced existing directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failure changed destination or leaked staging: %v, %v", entries, err)
	}
}
