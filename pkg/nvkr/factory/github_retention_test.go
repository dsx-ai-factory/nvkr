// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubUploadsHaveBoundedRetention(t *testing.T) {
	for _, test := range []struct {
		name        string
		plan        func(*testing.T) *Plan
		handoffDays string
		diagnostics bool
	}{
		{"development", githubDevelopmentTestPlan, "1", false},
		{"signed development", githubDevelopmentPublicationTestPlan, "7", false},
		{"hosted", githubTestPlan, "7", false},
		{"per-output compact workflow", referenceTestPlan, "7", false},
		{"native disk amd64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/amd64") }, "7", true},
		{"native disk arm64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }, "7", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			counts := map[string]int{}
			for jobID, job := range document.Jobs {
				for _, step := range job.Steps {
					if step.Uses != plan.Actions["upload-artifact"].Uses() {
						continue
					}
					kind, want := "carrier", test.handoffDays
					switch {
					case step.With["name"] == "nvkr-lifecycle-run":
						kind, want = "record", "7"
					case step.With["path"] == "nvkr-exec":
						kind = "helper"
					case step.Name == "Retain inspection diagnostics" || step.Name == "Upload failed Packer diagnostics":
						kind, want = "diagnostic", "1"
					}
					counts[kind]++
					if step.With["retention-days"] != want {
						t.Fatalf("%s/%s retention = %q, want %s", jobID, step.Name, step.With["retention-days"], want)
					}
					if kind != "diagnostic" && step.With["if-no-files-found"] != "error" {
						t.Fatalf("%s/%s permits a missing required upload", jobID, step.Name)
					}
				}
			}
			if counts["helper"] == 0 || counts["carrier"] == 0 || counts["record"] != 1 {
				t.Fatalf("missing required upload families: %v", counts)
			}
			if test.diagnostics && counts["diagnostic"] != 2 {
				t.Fatalf("disk fixture omits diagnostic uploads: %v", counts)
			}
		})
	}
}

func TestValidateGitHubBundleRejectsUploadRetentionTampering(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", githubTestPlan},
		{"development", githubDevelopmentTestPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			testGitHubRetentionTampering(t, test.plan(t))
		})
	}
}

func testGitHubRetentionTampering(t *testing.T, plan *Plan) {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	// Enumerate logical uploads, then change the field in its owning member.
	// Keeping the member set intact exercises retention admission, not drift.
	type upload struct {
		jobID string
		step  githubStep
	}
	steps := map[string]upload{}
	for _, jobID := range sortedKeys(document.Jobs) {
		for _, step := range document.Jobs[jobID].Steps {
			if step.Uses != plan.Actions["upload-artifact"].Uses() {
				continue
			}
			kind := "carrier"
			if step.With["name"] == "nvkr-lifecycle-run" {
				kind = "record"
			} else if step.With["path"] == "nvkr-exec" {
				kind = "helper"
			}
			if _, found := steps[kind]; !found {
				steps[kind] = upload{jobID, step}
			}
		}
	}
	for _, kind := range []string{"helper", "carrier", "record"} {
		selected, ok := steps[kind]
		if !ok {
			t.Fatalf("fixture omits %s upload", kind)
		}
		for _, mutation := range []struct{ name, value string }{
			{"removed", ""},
			{"repository default", "0"},
			{"long-lived", "90"},
		} {
			t.Run(kind+"/"+mutation.name, func(t *testing.T) {
				var changed *GeneratedBundle
				if kind == "carrier" {
					_, part, role := githubTestSharedUpload(t, bundle, selected.jobID, selected.step.Name)
					changed = mutateGitHubActionParameters(t, bundle, selected.jobID, part, func(values map[string]string) {
						field := role + "_with_retention_days"
						if values[field] != selected.step.With["retention-days"] || values[field+"_present"] != "true" {
							t.Fatal("carrier descriptor does not match its expanded retention")
						}
						if mutation.value == "" {
							values[field+"_present"] = "false"
						} else {
							values[field] = mutation.value
						}
					})
				} else {
					member := bundle.WorkflowPath
					if kind == "helper" {
						var jobID string
						jobID, member = githubTestBootstrap(t, bundle)
						if jobID != selected.jobID {
							t.Fatal("selected helper upload differs from its bootstrap owner")
						}
					}
					changed = mutateGitHubBundleYAML(t, bundle, member, func(root *yamlv3.Node) {
						var sequence *yamlv3.Node
						if kind == "helper" {
							sequence = githubTestYAMLField(t, root, "runs", "steps")
						} else {
							sequence = githubTestYAMLField(t, root, "jobs", selected.jobID, "steps")
						}
						step := githubTestYAMLStep(t, sequence, "name", selected.step.Name)
						with := githubTestYAMLField(t, step, "with")
						retention := githubTestYAMLField(t, with, "retention-days")
						if retention.Value != selected.step.With["retention-days"] {
							t.Fatal("upload member does not match its expanded retention")
						}
						if mutation.value == "" {
							githubYAMLRemove(with, "retention-days")
						} else {
							retention.Value = mutation.value
						}
					})
				}
				err := ValidateGitHubBundle(t.Context(), plan, changed)
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if !strings.Contains(err.Error(), "exact admitted rendering") {
					t.Fatalf("retention mutation reached the wrong admission check: %v", err)
				}
			})
		}
	}
}

// Resolve one logical upload to its actual descriptor slot and shared template.
// A job may invoke the same template repeatedly, so its path alone is not enough.
func githubTestSharedUpload(t *testing.T, bundle *GeneratedBundle, jobID, stepName string) (member, part, role string) {
	t.Helper()
	var data githubActionDataDocument
	if err := canonical.UnmarshalExact(bundle.Files[path.Join(path.Dir(bundle.PlanPath), "action-data.json")], &data); err != nil {
		t.Fatal(err)
	}
	job, exists := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs[jobID]
	if !exists {
		t.Fatalf("fixture omits upload job %q", jobID)
	}
	for _, call := range job.Steps {
		binding := call.With["parameters"]
		if binding == "" {
			continue
		}
		prefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
		key := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
		if binding != githubParametersExpression(key) {
			t.Fatalf("fixture job %q has an unknown parameter binding", jobID)
		}
		var values map[string]string
		if err := canonical.UnmarshalExact([]byte(data.Entries[jobID].Values[key]), &values); err != nil {
			t.Fatal(err)
		}
		for _, candidate := range []string{"upload", "extra_upload"} {
			if values[candidate+"_present"] != "true" || values[candidate+"_name"] != stepName {
				continue
			}
			if member != "" {
				t.Fatalf("fixture job %q repeats upload %q", jobID, stepName)
			}
			member, part, role = strings.TrimPrefix(call.Uses, "./")+"/action.yml", key, candidate
		}
	}
	if member == "" || len(bundle.Files[member]) == 0 {
		t.Fatalf("fixture job %q omits shared upload %q", jobID, stepName)
	}
	return member, part, role
}
