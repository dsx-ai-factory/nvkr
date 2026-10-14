// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package qualification

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestQualificationGeneratedNonregularFiles(t *testing.T) {
	for _, name := range []string{"fifo workflow", "symlink owner-only lock"} {
		t.Run(name, func(t *testing.T) {
			bundle, policy := generationBundle(t)
			root := t.TempDir()
			mustWriteQualification(t, root, bundle)
			if name == "fifo workflow" {
				target := filepath.Join(root, bundle.manifest.SmokeWorkflow)
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(target, 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				lock := filepath.Join(root, QualificationManifestPath+".lease")
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				operator := filepath.Join(root, "operator")
				if err := os.WriteFile(operator, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(operator, lock); err != nil {
					t.Fatal(err)
				}
			}
			requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), e.CodeWriteConflict)
			requireCode(t, WriteWorkflows(t.Context(), root, bundle), e.CodeWriteConflict)
		})
	}
}
