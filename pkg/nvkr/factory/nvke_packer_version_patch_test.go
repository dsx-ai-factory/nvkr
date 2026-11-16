// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The pinned NVKE Packer project accepts one exact Packer version and one exact
// QEMU plugin version, and the toolchain image carries others, so `packer
// validate` refuses the unpatched project. A catalog patch moves the project's
// pins to the producer's declared Packer version and the toolchain lock's
// plugin version. A toolchain refresh that changes one side without the patch
// fails here instead of in a hosted disk build. A producer on another Packer
// selection, such as the nightly one that matches NVKE's own pins, must stay
// unpatched. Remove this test with that patch.
func TestMaintainedDiskProducersPatchPackerVersion(t *testing.T) {
	raw, err := os.ReadFile("../../../toolchains/nvkr-packer/toolchain.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packer struct {
			Version string `json:"version"`
		} `json:"packer"`
		QEMUPlugin struct {
			Version string `json:"version"`
		} `json:"qemuPlugin"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil || lock.Packer.Version == "" || lock.QEMUPlugin.Version == "" {
		t.Fatalf("toolchain lock declares no Packer or QEMU plugin version: %v", err)
	}
	plugin := `+      version = "= ` + lock.QEMUPlugin.Version + `"` + "\n"
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
				if node.Packer.PackerVersion != lock.Packer.Version {
					if slices.ContainsFunc(node.Patches, func(patch ir.PatchRef) bool {
						return strings.Contains(patch.Content, "required_version")
					}) {
						t.Errorf("%s: %s selects Packer %s, not the toolchain lock's %s, but carries a version-pin patch",
							path, node.ID, node.Packer.PackerVersion, lock.Packer.Version)
					}
					continue
				}
				pin := `+  required_version = "= ` + node.Packer.PackerVersion + `"` + "\n"
				if !slices.ContainsFunc(node.Patches, func(patch ir.PatchRef) bool {
					return strings.Contains(patch.Content, pin) && strings.Contains(patch.Content, plugin)
				}) {
					t.Errorf("%s: %s has no patch that pins the project to Packer %s and QEMU plugin %s",
						path, node.ID, node.Packer.PackerVersion, lock.QEMUPlugin.Version)
				}
			}
		}
	}
	if producers == 0 {
		t.Fatal("no maintained disk producer found")
	}
}
