// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestGPUCompositeQualificationPairs(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, plan, policy, owner, trigger string }{
		{"production", ".github/nvkr-run-nvke-gpu-site/plan.json", "nvke-gpu-site-qualification.yaml", ".github/nvkr-qualification-nvke-gpu-site/manifest.json", "nvkr-project-nvke-gpu-site"},
		{"floating candidate", ".github/nvkr-run-nvke-nightly-gpu-site/plan.json", "nvke-nightly-gpu-site-qualification.yaml", ".github/nvkr-qualification-nvke-nightly-gpu-site/manifest.json", "nvkr-project-nvke-nightly-gpu-site"},
		{"development", ".github/nvkr-dev/plan.json", "nvke-gpu-site-development-qualification.yaml", DevelopmentQualificationManifestPath, "workflow_dispatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := factory.LoadPlanContext(t.Context(), "../../../"+tc.plan)
			if err != nil {
				t.Fatal(err)
			}
			policyPath := "inputs/policies/" + tc.policy
			policy, err := lifecycle.LoadQualificationPolicy(t.Context(), "../../../"+policyPath)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Composition == nil {
				t.Fatal("fixture must be composite")
			}
			pair, err := RenderWorkflows(t.Context(), plan, policy, policyPath)
			if err != nil {
				t.Fatal(err)
			}
			if pair.ManifestPath() != tc.owner || !strings.Contains(string(pair.files[policy.QualificationWorkflow]), tc.trigger) {
				t.Fatal("wrong owner or producer trigger")
			}
			if tc.name == "floating candidate" {
				workflow := string(pair.files[policy.QualificationWorkflow])
				if !strings.Contains(workflow, "retention-days: 14") || strings.Contains(workflow, "retention-days: 7") {
					t.Fatal("floating candidate qualification outcome must retain 14 days")
				}
			}
			if err := WriteWorkflows(t.Context(), root, pair); err != nil {
				t.Fatal(err)
			}
			if err := CheckWorkflows(t.Context(), root, pair); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
				t.Fatal(err)
			}
			policy.Smoke.NonToolOutputs = policy.Smoke.NonToolOutputs[1:]
			_, err = RenderWorkflows(t.Context(), plan, policy, policyPath)
			requireCode(t, err, e.CodeGateUnmet)
		})
	}
}
