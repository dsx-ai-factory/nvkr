// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNightlyProducerFinalizerPreservesNamedPlanAndQualificationHandoff(t *testing.T) {
	profiles := []map[string]string{
		{"name": "nvke-nightly-arm64", "plan": ".github/nvkr-run-nvke-nightly-arm64/plan.json"},
		{"name": "nvke-nightly-upstream-ovn-arm64", "plan": ".github/nvkr-run-nvke-nightly-upstream-ovn-arm64/plan.json"},
	}
	for _, profile := range profiles {
		t.Run(profile["name"], func(t *testing.T) {
			plan, err := LoadPlanContext(t.Context(), filepath.Join("../../..", profile["plan"]))
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			job := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs["nvkr-lifecycle-run"]
			runs := joinRuns(job.Steps)
			if !strings.Contains(runs, "--plan "+shellQuote(profile["plan"])) ||
				!strings.Contains(runs, " --handoff "+githubQualificationHandoffPath) ||
				strings.Contains(runs, "--plan "+shellQuote(FactoryPlanPath)) {
				t.Fatal("nightly finalizer lost its named Plan or complete qualification handoff")
			}
			uploads := 0
			for _, step := range job.Steps {
				if step.With["name"] == githubQualificationHandoffName {
					uploads++
					if step.If != "${{ success() }}" || step.With["path"] != githubQualificationHandoffPath {
						t.Fatal("nightly qualification handoff upload escaped the successful finalizer")
					}
				}
			}
			if uploads != 1 {
				t.Fatal("nightly finalizer omitted or duplicated its complete qualification handoff")
			}
		})
	}
}
