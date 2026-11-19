// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRuntimePublicationRequiresCompleteProducerEvidence(t *testing.T) {
	for _, mode := range []string{"", "per-output-v1"} {
		t.Run(mode, func(t *testing.T) {
			input, environment, base, stacks := runtimeConsumerInputsFixture(t, []string{"first", "second"})
			published := diskPublicationPlanFixture(t, "linux/amd64")
			environment.Signing = published.Environment.Signing
			environment.DiskInspectionProfiles = published.Environment.DiskInspectionProfiles
			environment.ToolchainAccess.PublicRoots = append(environment.ToolchainAccess.PublicRoots, "example.test/inspection")
			slices.Sort(environment.ToolchainAccess.PublicRoots)
			input.CosignTool = "cosign"
			base.Catalog.Tools = published.CatalogSnapshot.Tools
			component := base.Catalog.Components["image"]
			component.Packer.AcquisitionArchive = "test-acquisitions.tar"
			base.Catalog.Components["image"] = component
			base, err := catalog.ApplyStack(base.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			environment.Publication = &definition.Publication{Mode: mode, RepositoryPrefix: "registry.example.test/runtime", Anonymous: true}
			plan, err := compileWithTargetStacks(t.Context(), input, environment, base, stacks, "test", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != 2 {
				t.Fatal("internal runtime became a delivered output")
			}
			publication := "package/release"
			if mode != "" {
				publication = "package/publication-first"
			}
			checked := false
			for i := range plan.Graph.Nodes {
				node := &plan.Graph.Nodes[i]
				if node.ID != publication {
					continue
				}
				checked = true
				original := slices.Clone(node.Inputs)
				for _, id := range original {
					if strings.HasPrefix(id, "source/") || strings.HasPrefix(id, "source-receipt/") || strings.HasPrefix(id, "artifact/internal/runtime-") {
						t.Fatalf("publication transported execution input %s instead of its evidence", id)
					}
				}
				for _, id := range []string{
					"sbom/internal/runtime-first",
					"result/internal/runtime-first/packer-amd64",
					"result/internal/runtime-first/packer-plugin-qemu-amd64",
					"result/internal/runtime-first/vm-runtime-amd64",
				} {
					if !slices.Contains(original, id) {
						t.Fatalf("%s omits runtime closure evidence %s", node.ID, id)
					}
					node.Inputs = slices.DeleteFunc(slices.Clone(original), func(input string) bool { return input == id })
					assertFactoryErrorCode(t, plan.validatePublicationEvidence(), e.CodeProvenanceMissing)
					node.Inputs = original
				}
				if mode != "" && slices.Contains(original, "result/internal/runtime-second/packer-amd64") {
					t.Fatal("per-output publication acquired a sibling runtime's evidence")
				}
			}
			if !checked {
				t.Fatalf("missing publication %s", publication)
			}
		})
	}
}
