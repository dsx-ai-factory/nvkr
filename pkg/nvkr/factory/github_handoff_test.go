// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
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
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			job := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath]).Jobs["nvkr-lifecycle-run"]
			if strings.Contains(joinRuns(job.Steps), " --handoff .nvkr/qualification/handoff.json") != test.want {
				t.Fatalf("handoff capture admission differs for %s", test.name)
			}
			uploads := 0
			for _, step := range job.Steps {
				if step.With["name"] != "nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}" {
					continue
				}
				uploads++
				if step.Uses != plan.Actions["upload-artifact"].Uses() || step.If != "${{ success() }}" || step.TimeoutMinutes != 5 || step.ContinueOnError || len(step.Env) != 0 || len(step.With) != 4 ||
					step.With["path"] != ".nvkr/qualification/handoff.json" || step.With["include-hidden-files"] != "true" || step.With["if-no-files-found"] != "error" {
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
	for _, test := range []struct{ name, field, old, replacement string }{
		{"unchanged", "", "", ""},
		{"wrong attempt", "with.name", githubQualificationHandoffName, "nvkr-qualification-latest"},
		{"whole workspace", "with.path", githubQualificationHandoffPath, ".nvkr"},
		{"failure upload", "if", "${{ success() }}", "${{ always() }}"},
		{"no deadline", "timeout-minutes", "5", ""},
		{"missing capture", "run", " --handoff .nvkr/qualification/handoff.json", ""},
		{"hidden omission", "with.include-hidden-files", "true", "false"},
		{"missing handoff ignored", "with.if-no-files-found", "error", "ignore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := bundle.Files[GitHubWorkflowPath]
			workflow := bytes.Clone(original)
			if test.field != "" {
				var tree yamlv3.Node
				if err := yamlv3.Unmarshal(original, &tree); err != nil {
					t.Fatal(err)
				}
				mutated := false
				var mutate func(*yamlv3.Node)
				mutate = func(node *yamlv3.Node) {
					if mutated {
						return
					}
					if test.name == "missing capture" && node.Kind == yamlv3.ScalarNode && strings.Contains(node.Value, test.old) {
						node.Value = strings.Replace(node.Value, test.old, test.replacement, 1)
						mutated = true
						return
					}
					if node.Kind == yamlv3.MappingNode && test.name != "missing capture" {
						for i := 0; i < len(node.Content); i += 2 {
							if node.Content[i].Value != "name" || node.Content[i+1].Value != "Upload complete qualification handoff" {
								continue
							}
							// Locate the exact upload field independently of whole-job
							// compaction, scalar quoting and unrelated step fields.
							parent := node
							fields := strings.Split(test.field, ".")
							for depth, field := range fields {
								found := false
								for j := 0; j < len(parent.Content); j += 2 {
									if parent.Content[j].Value != field {
										continue
									}
									found = true
									if depth < len(fields)-1 {
										parent = parent.Content[j+1]
										break
									}
									if parent.Content[j+1].Value != test.old {
										t.Fatalf("upload field %s = %q, want %q", test.field, parent.Content[j+1].Value, test.old)
									}
									if test.replacement == "" {
										parent.Content = append(parent.Content[:j], parent.Content[j+2:]...)
									} else {
										parent.Content[j+1].Value = test.replacement
									}
									mutated = true
									return
								}
								if !found {
									t.Fatalf("upload field %s is missing", test.field)
								}
							}
						}
					}
					for _, child := range node.Content {
						mutate(child)
					}
				}
				mutate(&tree)
				if !mutated {
					t.Fatal("mutation did not apply")
				}
				raw, err := yamlv3.Marshal(&tree)
				if err != nil {
					t.Fatal(err)
				}
				workflow, err = compactGitHubWorkflow(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateGitHubWorkflowSize(workflow); err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(workflow, original) {
					t.Fatal("mutation did not change workflow")
				}
			}
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			changed.Files[GitHubWorkflowPath], changed.WorkflowDigest = workflow, pipeline.FileDigest(workflow)
			err := ValidateGitHubBundle(t.Context(), plan, &changed)
			if test.name == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
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
