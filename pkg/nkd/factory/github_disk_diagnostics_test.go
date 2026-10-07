// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubDiskDiagnosticsAndDeadlineSemantics(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, mutation := range []string{"valid", "missing", "duplicate", "always upload", "substituted readiness output", "workspace path", "substituted route output", "long retention", "ambient credentials", "unbounded upload", "changed upload duration", "late upload", "outer equals inner"} {
			t.Run(platform+"/"+mutation, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				stages, _, err := githubStages(plan)
				if err != nil {
					t.Fatal(err)
				}
				var stage githubStage
				for _, candidate := range stages {
					if candidate.DiskOutput != "" {
						stage = candidate
					}
				}
				steps := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath]).Jobs[stage.ID].Steps
				index := slices.IndexFunc(steps, func(s githubStep) bool { return s.Name == "Upload failed Packer diagnostics" })
				if index < 0 {
					t.Fatal("no diagnostic upload")
				}
				switch mutation {
				case "missing":
					steps = slices.Delete(steps, index, index+1)
				case "duplicate":
					steps = append(steps, steps[index])
				case "always upload":
					steps[index].If = "${{ always() }}"
				case "substituted readiness output":
					steps[index].If = "${{ failure() && steps.other.outputs.packer_diagnostics != '' }}"
				case "workspace path":
					steps[index].With["path"] = "."
				case "substituted route output":
					steps[index].With["path"] = "${{ steps.other.outputs.packer_diagnostics }}"
				case "long retention":
					steps[index].With["retention-days"] = "90"
				case "ambient credentials":
					steps[index].Env = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}
				case "unbounded upload":
					steps[index].TimeoutMinutes = 0
				case "changed upload duration":
					steps[index].TimeoutMinutes = githubDiskDiagnosticUploadMinutes + 1
				case "late upload":
					steps[index-1], steps[index] = steps[index], steps[index-1]
				case "outer equals inner":
					steps[index-1].TimeoutMinutes = int(stage.diskExecutionBudget / time.Minute)
				}
				err = validateGitHubStageExecution(plan, stage, steps)
				if mutation == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				}
			})
		}
	}
}
