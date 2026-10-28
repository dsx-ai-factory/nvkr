// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublishImageExportRejectsUnboundInputs(t *testing.T) {
	for _, args := range [][]string{
		{"publish", "image-export", "--layout", "missing", "--object", "cwe", "--out", "image.tar"},
		{"publish", "image-export", "--layout", "missing", "--object", "cwe", "--reference", "registry.example.test/cwe:latest", "--out", "image.tar"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			t.Chdir(t.TempDir())
			_, err := invokeLifecycleCLI(t, args...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			files, err := os.ReadDir(".")
			if err != nil || len(files) != 0 {
				t.Fatalf("unbound image export wrote output: %v %v", files, err)
			}
		})
	}
}
