// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"
)

func TestNamedProducerFinalizerPreservesPlanAndQualificationHandoff(t *testing.T) {
	for _, name := range []string{"project-a", "project-b"} {
		t.Run(name, func(t *testing.T) {
			plan := referenceTestPlan(t)
			plan.Environment.GitHubBundle = name
			var err error
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			job := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle)).Jobs["nvkr-lifecycle-run"]
			runs := joinRuns(job.Steps)
			if !strings.Contains(runs, "--plan "+shellQuote(githubBundleLayout(name).planPath)) ||
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
