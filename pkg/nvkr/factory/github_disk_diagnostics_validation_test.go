// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestValidateGitHubBundleRejectsInspectionDiagnosticUploadTampering(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatalf("unmodified diagnostic upload was rejected: %v", err)
			}
			original := expandedGitHubWorkflow(t, bundle)
			baseline, err := yaml.Marshal(decodeGitHubWorkflow(t, original))
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name   string
				mutate func(*githubJob, int, int)
			}{
				{"wildcard path", func(job *githubJob, diagnostic, _ int) {
					job.Steps[diagnostic].With["path"] = ".nvkr/diagnostics/inspection/*.jsonl"
				}},
				{"carrier path", func(job *githubJob, diagnostic, payload int) {
					job.Steps[diagnostic].With["path"] = job.Steps[payload].With["path"]
				}},
				{"artifact name", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].With["name"] = "unexpected-diagnostics" }},
				{"step name", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].Name = "Upload extra evidence" }},
				{"retention", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].With["retention-days"] = "2" }},
				{"condition", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].If = "${{ success() }}" }},
				{"credential", func(job *githubJob, diagnostic, _ int) {
					job.Steps[diagnostic].Env = map[string]string{"NVKR_CREDENTIAL": "${{ secrets.UNRELATED }}"}
				}},
				{"continue on error", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].ContinueOnError = false }},
				{"unbounded upload", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].TimeoutMinutes = 0 }},
				{"longer upload", func(job *githubJob, diagnostic, _ int) { job.Steps[diagnostic].TimeoutMinutes++ }},
				{"required payload warning", func(job *githubJob, _, payload int) { job.Steps[payload].With["if-no-files-found"] = "warn" }},
				{"removed diagnostics", func(job *githubJob, diagnostic, _ int) {
					job.Steps = append(job.Steps[:diagnostic:diagnostic], job.Steps[diagnostic+1:]...)
				}},
				{"extra diagnostics", func(job *githubJob, diagnostic, _ int) { job.Steps = append(job.Steps, job.Steps[diagnostic]) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					document := decodeGitHubWorkflow(t, original)
					found := 0
					for id, job := range document.Jobs {
						for diagnostic, step := range job.Steps {
							if step.Name != "Retain inspection diagnostics" {
								continue
							}
							found++
							payload := -1
							for index, upload := range job.Steps {
								if upload.Name != "Upload failed Packer diagnostics" && upload.Uses == step.Uses && upload.With["if-no-files-found"] == "error" {
									payload = index
									break
								}
							}
							if payload <= diagnostic || step.With["path"] == "" || step.With["name"] == "" {
								t.Fatal("fixture lacks distinct diagnostic and required payload uploads")
							}
							test.mutate(&job, diagnostic, payload)
							document.Jobs[id] = job
							break
						}
					}
					if found != 1 {
						t.Fatalf("fixture must have exactly one diagnostic upload, got %d", found)
					}
					workflow, err := yaml.Marshal(document)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Equal(workflow, baseline) {
						t.Fatal("mutation did not change workflow semantics")
					}
					changed := relowerGitHubTestBundle(t, plan, bundle, workflow)
					// Authenticate the hostile bytes so rejection exercises
					// semantic admission rather than a stale workflow digest.
					assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
				})
			}
		})
	}
}
