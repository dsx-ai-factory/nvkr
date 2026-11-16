// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublishRejectsRetiredReleaseMetadataBeforeIO(t *testing.T) {
	for _, test := range []struct {
		command string
		flag    string
		inputs  []string
	}{
		{"root", "--inventory", []string{"--bundle", "demo=missing"}},
		{"root", "--release-set", []string{"--bundle", "demo=missing"}},
		{"bundle", "--inventory-digest", []string{"--file", "demo=missing", "--arch", "amd64"}},
		{"bundle", "--release-set-digest", []string{"--file", "demo=missing", "--arch", "amd64"}},
		{"apt", "--inventory-digest", []string{"--package", "missing.deb"}},
		{"apt", "--release-set-digest", []string{"--package", "missing.deb"}},
	} {
		t.Run(test.command+test.flag, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			args := []string{"publish", test.command, test.flag, "retired", "--out", filepath.Join(root, "publication")}
			_, err := invokeLifecycleCLI(t, append(args, test.inputs...)...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected metadata created output: %v, %v", entries, err)
			}
		})
	}
}
