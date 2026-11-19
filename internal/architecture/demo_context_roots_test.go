// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package architecture

import (
	"path/filepath"
	"testing"
)

// Reuse the source-only scanner without linking the nested demo into core tests.
func TestDemoContextRootsMatchExactInventory(t *testing.T) {
	exceptions := map[contextRoot]contextRootException{
		{"cmd/nvke-demo/main.go", "main", "Background"}:                          {1, processRoot},
		{"cmd/nvkr-site-identity-helper/main.go", "main", "Background"}:          {1, processRoot},
		{"internal/nvkedemo/support.go", "(*OwnedProcess).finish", "Background"}: {3, boundedCleanup},
	}
	found, err := findModuleContextRoots(filepath.Join("..", "..", "demos", "nvke"))
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range contextRootViolations(found, exceptions) {
		t.Error(violation)
	}
}
