// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"sigs.k8s.io/yaml"
)

func TestDeferredDiskEvidenceRejectsInspectionDiagnosticUpload(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			base := arcTestPlan(t, platform)
			environment := base.Environment.Clone()
			environment.DiskEvidence = sbom.DiskBuildProvenance
			environment.DiskInspectionProfiles = nil
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), base.Definition, environment, stack, "dev (unknown)")
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatalf("deferred mode without an inspector was rejected: %v", err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			for _, job := range document.Jobs {
				for _, step := range job.Steps {
					if step.Name == "Retain inspection diagnostics" || strings.HasPrefix(step.With["path"], ".nvkr/diagnostics/inspection/") {
						t.Fatal("deferred mode emitted an inspection diagnostic upload")
					}
				}
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			injected := 0
			for _, stage := range stages {
				if stage.DiskOutput == "" {
					continue
				}
				for _, image := range githubStagePrivateImages(plan, stage) {
					if strings.Contains(image, "inspector") {
						t.Fatal("deferred mode requested a private inspector image")
					}
				}
				job := document.Jobs[stage.ID]
				job.Steps = append(job.Steps, githubInspectionDiagnosticStep(testActionRefs(t, plan), stage.DiskOutput))
				document.Jobs[stage.ID] = job
				injected++
			}
			if injected != 1 {
				t.Fatalf("fixture requires exactly one disk stage, got %d", injected)
			}
			workflow, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			changed := relowerGitHubTestBundle(t, plan, bundle, workflow)
			err = ValidateGitHubBundle(t.Context(), plan, changed)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			// Reject the missing-file warning at semantic admission before
			// exact regeneration notices the serializer's formatting changes.
			if !strings.Contains(err.Error(), "permits a missing declared output") {
				t.Fatalf("injected diagnostic bypassed the deferred-mode upload guard: %v", err)
			}
		})
	}
}
