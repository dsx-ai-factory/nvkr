// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMaintainedDomainCatalogPlansFitReadBound(t *testing.T) {
	for _, bundle := range []string{"nvkr-run", "nvkr-dev", "nvkr-run-nvke-gpu-site", "nvkr-run-nvke-nightly-gpu-site"} {
		t.Run(bundle, func(t *testing.T) {
			path := filepath.Join("../../..", ".github", bundle, "plan.json")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() > MaxPlanBytes {
				t.Fatalf("maintained Plan is %d bytes, limit %d", info.Size(), MaxPlanBytes)
			}
			if _, err := LoadPlanContext(t.Context(), path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanReadRejectsOversizedDocumentBeforeDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, MaxPlanBytes+1); err != nil {
		t.Fatal(err)
	}
	plan, err := LoadPlanContext(t.Context(), path)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if plan != nil {
		t.Fatal("oversized Plan admitted")
	}
}
