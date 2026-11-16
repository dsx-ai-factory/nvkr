// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The pinned NVKE Packer project accepts one exact Packer version and the
// toolchain image carries another, so `packer validate` refuses the unpatched
// project. A catalog patch moves the project's pin to the producer's declared
// version. A toolchain refresh that changes one without the other fails here
// instead of in a hosted disk build. Remove this test with that patch.
func TestMaintainedDiskProducersPatchPackerVersion(t *testing.T) {
	plans, err := filepath.Glob("../../../.github/*/plan.json")
	if err != nil || len(plans) == 0 {
		t.Fatalf("no committed Plan found: %v (%v)", plans, err)
	}
	producers := 0
	for _, path := range plans {
		plan, err := LoadPlanContext(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		for _, build := range plan.Builds {
			for _, node := range build.Graph.Nodes {
				if node.Packer == nil {
					continue
				}
				producers++
				pin := `+  required_version = "= ` + node.Packer.PackerVersion + `"`
				if !slices.ContainsFunc(node.Patches, func(patch ir.PatchRef) bool {
					return strings.Contains(patch.Content, pin+"\n")
				}) {
					t.Errorf("%s: %s has no patch that pins the project to Packer %s", path, node.ID, node.Packer.PackerVersion)
				}
			}
		}
	}
	if producers == 0 {
		t.Fatal("no maintained disk producer found")
	}
}
