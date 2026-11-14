// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRemovedQualifiedCommandsRejectBeforeWork(t *testing.T) {
	for _, args := range [][]string{
		{"build"}, {"assemble"}, {"attest"}, {"verify"}, {"profile"},
		{"fetch"}, {"bake-off"}, {"sbom"}, {"disk"},
		{"release", "promote"}, {"release", "clear"}, {"release", "select"},
		{"release", "rollback"}, {"release", "advisory"}, {"release", "status"},
		{"release", "verify-set"}, {"release", "policy"}, {"release", "checkpoint"},
		{"release", "from-vectors"}, {"release", "withdraw"},
		{"release", "supersede"}, {"release", "deprecate"},
	} {
		t.Run(filepath.Join(args...), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			app := newApp()
			app.Writer, app.ErrWriter = io.Discard, io.Discard
			err := app.RunContext(t.Context(), append([]string{"nvkr"}, args...))
			if cliErrorCode(err) != e.CodeUsage {
				t.Fatalf("removed command returned %v, want usage", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("removed command wrote output: %v, %v", entries, err)
			}
		})
	}
}

func TestReleaseLaneRejectedBeforeInputIO(t *testing.T) {
	for _, lane := range []string{"qualified", "unknown", ""} {
		t.Run(lane, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			app := newApp()
			app.Writer, app.ErrWriter = io.Discard, io.Discard
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "verify",
				"--reference", "https://127.0.0.1:1/must-not-connect", "--lane", lane,
				"--scope", "artifact-publication", "--definition", "missing.yaml",
				"--environment", "missing.yaml", "--plan", "missing.json", "--workspace", root,
			})
			if cliErrorCode(err) != e.CodeUsage {
				t.Fatalf("invalid lane returned %v, want usage", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid lane wrote output: %v, %v", entries, err)
			}
		})
	}
}
