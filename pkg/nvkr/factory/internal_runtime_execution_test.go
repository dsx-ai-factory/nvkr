// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// This fixture exercises the production-owner boundary, not complete consumer
// compilation. The runtime consumer Environment binding remains a separate gate.
func runtimeOwnerPlanFixture(t *testing.T) *Plan {
	t.Helper()
	directory, err := filepath.Abs("../../../toolchains/nvkr-vm-runtime")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	components := []string{"packer-amd64", "packer-plugin-qemu-amd64", "vm-runtime-amd64"}
	resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Platform: "linux/amd64", OutputFormat: definition.OutputOCI, Components: components})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d := definition.Definition{InternalTools: []definition.InternalTool{{Name: "runtime", Format: definition.OutputOCI,
		Platform: "linux/amd64", Components: components, Result: "vm-runtime-amd64"}}}
	builds := []BuildPlan{{Name: "internal/runtime", Platform: "linux/amd64", Components: components, Graph: graph, BuildDigest: digest, ResolvedComponents: resolved.Components}}
	selected, err := requiredInternalTools(d, builds)
	if err != nil {
		t.Fatal(err)
	}
	return &Plan{identityDigest: "sha256:" + strings.Repeat("a", 64), Definition: d, Builds: builds, InternalTools: selected}
}

func TestInternalRuntimeSelectionRequiresMkosiResult(t *testing.T) {
	plan := runtimeOwnerPlanFixture(t)
	if len(plan.InternalTools) != 1 || plan.InternalTools[0].Component != "vm-runtime-amd64" || len(plan.Builds[0].Components) != 3 {
		t.Fatal("runtime selection lost its static tool material closure")
	}
	plan.Definition.InternalTools[0].Result = "packer-amd64"
	_, err := requiredInternalTools(plan.Definition, plan.Builds)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestInternalRuntimeCannotRestoreProductionFromDescriptions(t *testing.T) {
	plan := runtimeOwnerPlanFixture(t)
	selected := plan.InternalTools[0]
	for _, tc := range []struct {
		name  string
		owner *LocalRunResult
		code  e.Code
	}{
		{"absent owner", nil, e.CodeVerifyFailed},
		{"foreign owner", &LocalRunResult{PlanDigest: "foreign"}, e.CodeVerifyFailed},
		{"closed owner", &LocalRunResult{PlanDigest: plan.identityDigest, diskClosed: true}, e.CodeGateUnmet},
		{"closing owner", &LocalRunResult{PlanDigest: plan.identityDigest, diskClosing: true}, e.CodeGateUnmet},
		{"zero production", &LocalRunResult{PlanDigest: plan.identityDigest}, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFactoryErrorCode(t, tc.owner.retainInternalRuntime(t.Context(), plan, selected, &pipeline.ProducedRuntime{}), tc.code)
			if tc.owner != nil && len(tc.owner.internalRuntimes) != 0 {
				t.Fatal("description populated runtime ownership")
			}
		})
	}
	owner := &LocalRunResult{PlanDigest: plan.identityDigest, Nodes: []LocalNodeResult{{ID: "internal/runtime/sbom", Status: "ok"}}}
	_, err := owner.borrowInternalRuntime(t.Context(), plan, selected.Name)
	assertFactoryErrorCode(t, err, e.CodeGateUnmet)
	assertFactoryErrorCode(t, owner.retainInternalExecutable(t.Context(), plan, selected, &pipeline.ProducedExecutable{}), e.CodeEngineCapability)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, owner.admitInternalRuntime(ctx, plan, selected), e.CodeTimeout)
}

func TestInternalRuntimeEvidenceRequiresLiveOriginalOwner(t *testing.T) {
	plan := runtimeOwnerPlanFixture(t)
	selected := plan.InternalTools[0]
	for _, kind := range []string{NodeVerify, NodeSBOM} {
		owner := &LocalRunResult{PlanDigest: plan.identityDigest, internalRuntimes: map[string]*pipeline.ProducedRuntime{selected.Name: {}}}
		node := Node{Kind: kind, BuildTarget: selected.BuildTarget}
		assertFactoryErrorCode(t, owner.admitInternalToolEvidence(t.Context(), plan, node), e.CodeGateUnmet)
		assertFactoryErrorCode(t, owner.recordInternalToolEvidence(t.Context(), plan, node), e.CodeGateUnmet)
		if len(owner.internalToolEvidence) != 0 {
			t.Fatal("unproduced runtime acquired completed evidence")
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if len(owner.internalRuntimes) != 0 {
			t.Fatal("Run close retained runtime ownership")
		}
	}
}

func TestInternalRuntimeUsesEmbeddedInventoryInsteadOfGlobalScanner(t *testing.T) {
	plan := runtimeOwnerPlanFixture(t)
	_, fixture := internalToolPlanFixture(t)
	pin := fixture.CatalogSnapshot.Tools["cosign"]
	pin.Platform = "linux/arm64"
	fixture.CatalogSnapshot.Tools["scanner"] = pin
	plan.Definition.SBOMTool = "scanner"
	environment := fixture.Environment.Clone()
	environment.Signing = nil
	environment.UtilityPlatform = "linux/arm64"
	environment.UtilityContainers = nil
	tools, err := selectedTools(plan.Definition, environment, fixture.CatalogSnapshot, plan.Builds)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools["scanner"].Platform != "linux/arm64" || plan.sbomToolForBuild("internal/runtime") != "" {
		t.Fatal("runtime inherited a foreign-platform scanner instead of its embedded reports")
	}
}
