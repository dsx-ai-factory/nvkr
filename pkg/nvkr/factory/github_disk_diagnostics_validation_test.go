// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
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
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			var jobID string
			var diagnostic, payload githubStep
			for id, job := range document.Jobs {
				for index, step := range job.Steps {
					if step.Name != "Retain inspection diagnostics" {
						continue
					}
					if jobID != "" {
						t.Fatal("fixture repeats inspection diagnostic upload")
					}
					jobID, diagnostic = id, step
					for position, upload := range job.Steps {
						if upload.Name == "Upload failed Packer diagnostics" || upload.Uses != step.Uses || upload.With["if-no-files-found"] != "error" {
							continue
						}
						if position <= index {
							t.Fatal("required payload upload must follow inspection diagnostics")
						}
						payload = upload
						break
					}
				}
			}
			if jobID == "" || diagnostic.With["path"] == "" || diagnostic.With["name"] == "" || payload.With["path"] == "" || payload.With["name"] == "" {
				t.Fatal("fixture lacks distinct diagnostic and required payload uploads")
			}
			member, part, role := githubTestSharedUpload(t, bundle, jobID, diagnostic.Name)
			if role != "upload" {
				t.Fatal("inspection diagnostics must use the standalone upload role")
			}
			_, payloadPart, payloadRole := githubTestSharedUpload(t, bundle, jobID, payload.Name)
			parameter := func(field, old, replacement string) func(*testing.T) *GeneratedBundle {
				return func(t *testing.T) *GeneratedBundle {
					return mutateGitHubActionParameters(t, bundle, jobID, part, func(values map[string]string) {
						if values[field] != old {
							t.Fatalf("diagnostic parameter %s = %q, want %q", field, values[field], old)
						}
						values[field] = replacement
					})
				}
			}
			caller := func(mutate func(*testing.T, *yamlv3.Node, *yamlv3.Node)) func(*testing.T) *GeneratedBundle {
				return func(t *testing.T) *GeneratedBundle {
					return mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
						steps := githubTestYAMLField(t, root, "jobs", jobID, "steps")
						mutate(t, steps, githubTestYAMLStep(t, steps, "name", diagnostic.Name))
					})
				}
			}
			for _, test := range []struct {
				name, reason string
				code         e.Code
				mutate       func(*testing.T) *GeneratedBundle
			}{
				// The attempt-name interface rejects a mismatched route before the
				// expanded workflow reaches upload admission.
				{"wildcard path", "unsupported provider-attempt name", e.CodeEngineCapability, parameter("upload_with_path", diagnostic.With["path"], ".nvkr/diagnostics/inspection/*.jsonl")},
				{"carrier path", "unsupported provider-attempt name", e.CodeEngineCapability, parameter("upload_with_path", diagnostic.With["path"], payload.With["path"])},
				{"artifact name", "missing declared output", e.CodeEngineCapability, func(t *testing.T) *GeneratedBundle {
					return mutateGitHubActionParameters(t, bundle, jobID, part, func(values map[string]string) {
						if values["upload_name_attempt"] != "true" || values["upload_with_name"]+githubAttemptUploadNameSuffix != diagnostic.With["name"] {
							t.Fatal("fixture lacks the exact diagnostic attempt name")
						}
						values["upload_name_attempt"], values["upload_with_name"] = "false", "unexpected-diagnostics"
					})
				}},
				{"step name", "missing declared output", e.CodeEngineCapability, parameter("upload_name", diagnostic.Name, "Upload extra evidence")},
				{"retention", "missing declared output", e.CodeEngineCapability, parameter("upload_with_retention_days", "1", "2")},
				{"condition", "requires exactly one transport step", e.CodeEngineCapability, caller(func(t *testing.T, _ *yamlv3.Node, step *yamlv3.Node) {
					githubTestYAMLField(t, step, "if").Value = "${{ success() }}"
				})},
				{"credential", "unexpected step credentials or environment", e.CodeEngineCapability, func(t *testing.T) *GeneratedBundle {
					return mutateGitHubBundleYAML(t, bundle, member, func(root *yamlv3.Node) {
						step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "operation_upload")
						if githubYAMLField(step, "env") != nil {
							t.Fatal("diagnostic transport already has an environment")
						}
						githubYAMLSet(step, "env", githubYAMLMap("NVKR_CREDENTIAL", githubYAMLString("${{ secrets.UNRELATED }}")))
					})
				}},
				{"continue on error", "missing declared output", e.CodeEngineCapability, caller(func(t *testing.T, _ *yamlv3.Node, step *yamlv3.Node) {
					githubTestYAMLField(t, step, "continue-on-error").Value = "false"
				})},
				{"unbounded upload", "missing declared output", e.CodeEngineCapability, caller(func(t *testing.T, _ *yamlv3.Node, step *yamlv3.Node) {
					githubTestYAMLField(t, step, "timeout-minutes")
					githubYAMLRemove(step, "timeout-minutes")
				})},
				{"longer upload", "missing declared output", e.CodeEngineCapability, caller(func(t *testing.T, _ *yamlv3.Node, step *yamlv3.Node) {
					githubTestYAMLField(t, step, "timeout-minutes").Value = strconv.Itoa(diagnostic.TimeoutMinutes + 1)
				})},
				{"required payload warning", "missing declared output", e.CodeEngineCapability, func(t *testing.T) *GeneratedBundle {
					return mutateGitHubActionParameters(t, bundle, jobID, payloadPart, func(values map[string]string) {
						field := payloadRole + "_with_if_no_files_found"
						if values[field] != "error" {
							t.Fatal("required payload does not reject missing files")
						}
						values[field] = "warn"
					})
				}},
				{"removed diagnostics", "exact admitted rendering", e.CodeEngineCapability, caller(func(t *testing.T, steps, step *yamlv3.Node) {
					for index, candidate := range steps.Content {
						if candidate == step {
							steps.Content = append(steps.Content[:index], steps.Content[index+1:]...)
							return
						}
					}
					t.Fatal("diagnostic call is not in its job")
				})},
				{"extra diagnostics", "exact admitted rendering", e.CodeEngineCapability, caller(func(_ *testing.T, steps, step *yamlv3.Node) {
					steps.Content = append(steps.Content, step)
				})},
				{"missing action member", "shared action \"" + member + "\" is missing", e.CodePipelineDrift, func(*testing.T) *GeneratedBundle {
					changed := *bundle
					changed.Files = maps.Clone(bundle.Files)
					delete(changed.Files, member)
					return &changed
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					err := ValidateGitHubBundle(t.Context(), plan, test.mutate(t))
					assertFactoryErrorCode(t, err, test.code)
					if !strings.Contains(err.Error(), test.reason) {
						t.Fatalf("diagnostic upload mutation reached the wrong guard: %v", err)
					}
				})
			}
		})
	}
}
