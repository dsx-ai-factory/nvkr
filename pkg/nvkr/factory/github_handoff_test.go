// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubQualificationHandoffRequiresCompleteSignedOutputPublication(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
		want bool
	}{
		{"unsigned", githubTestPlan, false},
		{"dry run", func(t *testing.T) *Plan { return signedTestPlan(t, dryRunSigningProfile(), ghcrTarget()) }, false},
		{"signed aggregate", func(t *testing.T) *Plan { return signedTestPlan(t, cosignSigningProfile(), ghcrTarget()) }, false},
		{"signed outputs", referenceTestPlan, true},
		{"exact runtime source", func(t *testing.T) *Plan {
			return recompileGitHubStabilityPlan(t, referenceTestPlan(t), func(in *githubStabilityInputs) {
				in.environment.GitHubBundle, in.environment.GitHubRuntimePlan = "exact-source", true
				in.environment.GitHubRuntimeSource = &authored.GitHubRuntimeSource{Repository: "https://github.com/containerd/containerd", Ref: "refs/heads/main"}
			})
		}, false},
		{"unsigned development", githubDevelopmentTestPlan, false},
		{"signed development", githubDevelopmentPublicationTestPlan, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			job := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle)).Jobs["nvkr-lifecycle-run"]
			if strings.Contains(joinRuns(job.Steps), " --handoff .nvkr/qualification/handoff.json") != test.want {
				t.Fatalf("handoff capture admission differs for %s", test.name)
			}
			uploads := 0
			for _, step := range job.Steps {
				if step.With["name"] != "nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}" {
					continue
				}
				uploads++
				if step.Uses != plan.Actions["upload-artifact"].Uses() || step.If != "${{ success() }}" || step.TimeoutMinutes != 5 || step.ContinueOnError || len(step.Env) != 0 || len(step.With) != 5 ||
					step.With["path"] != ".nvkr/qualification/handoff.json" || step.With["include-hidden-files"] != "true" || step.With["if-no-files-found"] != "error" || step.With["retention-days"] != "7" {
					t.Fatalf("handoff upload is not exact and bounded: %+v", step)
				}
			}
			wantUploads := 0
			if test.want {
				wantUploads = 1
			}
			if uploads != wantUploads {
				t.Fatalf("handoff upload count=%d want=%d", uploads, wantUploads)
			}
		})
	}
}

func TestGitHubQualificationHandoffRejectsWeakenedUpload(t *testing.T) {
	plan := referenceTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "nvkr-lifecycle-run"
	_, part, role := githubTestSharedUpload(t, bundle, jobID, "Upload complete qualification handoff")
	if role != "upload" {
		t.Fatal("handoff must use the standalone upload role")
	}
	for _, test := range []struct{ name, field, old, replacement, reason string }{
		{"unchanged", "", "", "", ""},
		{"wrong attempt", "with.name", githubQualificationHandoffName, "nvkr-qualification-latest", "complete handoff for this successful exact attempt"},
		{"whole workspace", "with.path", githubQualificationHandoffPath, ".nvkr", "unsupported provider-attempt name"},
		{"failure upload", "if", "${{ success() }}", "${{ always() }}", "must preserve its transport cancellation condition"},
		{"no deadline", "timeout-minutes", "5", "", "complete handoff for this successful exact attempt"},
		{"missing capture", "run", " --handoff .nvkr/qualification/handoff.json", "", "exact admitted rendering"},
		{"hidden omission", "with.include-hidden-files", "true", "false", "complete handoff for this successful exact attempt"},
		{"missing handoff ignored", "with.if-no-files-found", "error", "ignore", "missing declared output"},
		{"extended retention", "with.retention-days", "7", "90", "complete handoff for this successful exact attempt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := bundle
			switch {
			case strings.HasPrefix(test.field, "with."):
				changed = mutateGitHubActionParameters(t, bundle, jobID, part, func(values map[string]string) {
					field := "upload_with_" + strings.ReplaceAll(strings.TrimPrefix(test.field, "with."), "-", "_")
					old := values[field]
					if test.field == "with.name" {
						if values["upload_name_attempt"] != "true" {
							t.Fatal("fixture handoff name is not attempt-scoped")
						}
						old += githubAttemptUploadNameSuffix
						values["upload_name_attempt"] = "false"
					}
					if old != test.old || values[field+"_present"] != "true" {
						t.Fatalf("handoff parameter %s = %q, want %q", field, old, test.old)
					}
					// An altered path with the original attempt name is rejected by
					// the shared interface before downstream handoff validation.
					values[field] = test.replacement
				})
			case test.field != "":
				changed = mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
					steps := githubTestYAMLField(t, root, "jobs", jobID, "steps")
					if test.field == "run" {
						finalizer := githubTestYAMLStep(t, steps, "id", "finalize-catalog")
						run := githubTestYAMLField(t, finalizer, "run")
						if strings.Count(run.Value, test.old) != 1 {
							t.Fatal("finalizer does not capture the handoff exactly once")
						}
						run.Value = strings.Replace(run.Value, test.old, test.replacement, 1)
						return
					}
					step := githubTestYAMLStep(t, steps, "name", "Upload complete qualification handoff")
					field := githubTestYAMLField(t, step, test.field)
					if field.Value != test.old {
						t.Fatalf("handoff caller field %s = %q, want %q", test.field, field.Value, test.old)
					}
					if test.replacement == "" {
						githubYAMLRemove(step, test.field)
					} else {
						field.Value = test.replacement
					}
				})
			}
			err := ValidateGitHubBundle(t.Context(), plan, changed)
			if test.name == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("handoff mutation reached the wrong guard: %v", err)
			}
		})
	}
}

func TestReferenceFinalizerCarriesOriginalNamedOutputEvidence(t *testing.T) {
	plan := referenceTestPlan(t)
	_, artifacts := planIndex(plan)
	for _, output := range plan.LifecycleOutputs {
		route := outputPublicationRoute(output.Name)
		for _, prefix := range []string{"publication/", "signature/", "predicate/"} {
			artifact, exists := artifacts[prefix+route]
			if !exists || !finalizerArtifact(plan, artifact) {
				t.Fatalf("finalizer omits exact output evidence %s%s", prefix, route)
			}
		}
		for _, id := range []string{"package/" + output.Name, "package/" + outputPublicationPackage(output.Name), "metadata/" + route, "verification/" + route} {
			if artifact, exists := artifacts[id]; exists && finalizerArtifact(plan, artifact) {
				t.Fatalf("finalizer restored unneeded output artifact %s", id)
			}
		}
	}
}
