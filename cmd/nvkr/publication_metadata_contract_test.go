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
	for _, flag := range []string{"--inventory", "--release-set"} {
		t.Run(flag, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "publication")
			_, err := invokeLifecycleCLI(t, "publish", "root", flag, "missing.json", "--out", root)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("rejected metadata created publication: %v", err)
			}
		})
	}
}
