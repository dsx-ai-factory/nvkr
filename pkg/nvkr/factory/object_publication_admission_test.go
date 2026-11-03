// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRequiredObjectPublicationRefusesLocalBeforeWorkspace(t *testing.T) {
	for _, perOutput := range []bool{false, true} {
		_, plan := objectPublicationPlan(t, perOutput)
		t.Chdir(t.TempDir())
		workspace := filepath.Join(".nvkr", "must-not-exist")
		for _, dryRun := range []bool{false, true} {
			err := AdmitLocal(t.Context(), plan, LocalRunOptions{Workspace: workspace, Publish: true, DryRun: dryRun})
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if _, err := os.Stat(".nvkr"); !os.IsNotExist(err) {
				t.Fatal("unsupported local publication touched the workspace", err)
			}
		}
	}
}
