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
	for _, test := range []struct{ name, old, replacement string }{
		{"unchanged", "", ""},
		{"wrong attempt", "name: 'nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}'", "name: 'nvkr-qualification-latest'"},
		{"whole workspace", "path: .nvkr/qualification/handoff.json", "path: .nvkr"},
		{"failure upload", "if: '${{ success() }}'", "if: '${{ always() }}'"},
		{"no deadline", "timeout-minutes: 5, ", ""},
		{"missing capture", " --handoff .nvkr/qualification/handoff.json", ""},
		{"hidden omission", "include-hidden-files: 'true'", "include-hidden-files: 'false'"},
		{"missing handoff ignored", "if-no-files-found: error", "if-no-files-found: ignore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := bundle.Files[GitHubWorkflowPath]
			workflow := bytes.Clone(original)
			if test.old != "" {
				if test.name == "missing capture" {
					workflow = bytes.Replace(workflow, []byte(test.old), []byte(test.replacement), 1)
				} else {
					// Change only the admitted upload's compact YAML line. Preserve
					// all other exact rendered bytes, including YAML aliases.
					for _, line := range bytes.SplitAfter(original, []byte("\n")) {
						if bytes.Contains(line, []byte("name: Upload complete qualification handoff,")) {
							mutated := bytes.Replace(line, []byte(test.old), []byte(test.replacement), 1)
							workflow = bytes.Replace(original, line, mutated, 1)
							break
						}
					}
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
