// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestRuntimeUseCannotBeRestoredFromPublicRunDescriptions(t *testing.T) {
	plan := runtimeConsumerPlanFixture(t)
	planned, err := runtimePlanBinding(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		owner *LocalRunResult
		code  e.Code
	}{
		{"absent Run", nil, e.CodeUsage},
		{"foreign Run", &LocalRunResult{PlanDigest: "foreign"}, e.CodeVerifyFailed},
		{"copied public fields", &LocalRunResult{PlanDigest: planned.identity,
			Nodes: []LocalNodeResult{{ID: "internal/runtime/sbom", Status: "ok"}}}, e.CodeGateUnmet},
		{"closed Run", &LocalRunResult{PlanDigest: planned.identity, diskClosed: true}, e.CodeGateUnmet},
		{"missing private Plan", &LocalRunResult{PlanDigest: planned.identity,
			internalRuntimes: map[string]*pipeline.ProducedRuntime{"runtime": {}}}, e.CodeGateUnmet},
		{"zero production", &LocalRunResult{PlanDigest: planned.identity,
			internalRuntimes: map[string]*pipeline.ProducedRuntime{"runtime": {}}, internalRuntimePlans: map[string]runtimeProductionPlan{"runtime": planned}}, e.CodeGateUnmet},
		{"changed public Plan", &LocalRunResult{PlanDigest: planned.identity,
			internalRuntimes:     map[string]*pipeline.ProducedRuntime{"runtime": {}},
			internalRuntimePlans: map[string]runtimeProductionPlan{"runtime": {identity: "sha256:" + strings.Repeat("f", 64), current: planned.current}}}, e.CodeVerifyFailed},
		{"changed relocated Plan", &LocalRunResult{PlanDigest: planned.identity,
			internalRuntimes:     map[string]*pipeline.ProducedRuntime{"runtime": {}},
			internalRuntimePlans: map[string]runtimeProductionPlan{"runtime": {identity: planned.identity, current: "sha256:" + strings.Repeat("f", 64)}}}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, use, err := tc.owner.issueRuntimeUse(t.Context(), plan, "node-image/build-image")
			assertFactoryErrorCode(t, err, tc.code)
			if runtime != nil || use != nil {
				t.Fatal("descriptive Run state issued a runtime-use capability")
			}
			if tc.owner != nil && len(tc.owner.internalRuntimeUses) != 0 {
				t.Fatal("rejected Run retained a use capability")
			}
		})
	}
}

func TestRuntimeUseRequiresExactConsumerSelection(t *testing.T) {
	plan := runtimeConsumerPlanFixture(t)
	planned, err := runtimePlanBinding(plan)
	if err != nil {
		t.Fatal(err)
	}
	owner := &LocalRunResult{PlanDigest: planned.identity}
	for _, node := range []string{"unknown", "internal/runtime/sbom", "internal/runtime/build-vm-runtime-amd64"} {
		_, _, err := owner.issueRuntimeUse(t.Context(), plan, node)
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
}
