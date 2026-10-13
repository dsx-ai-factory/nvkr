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

func TestGitHubQualificationHandoffRequiresRealSigning(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
		want bool
	}{
		{"unsigned", githubTestPlan, false},
		{"dry run", func(t *testing.T) *Plan { return signedTestPlan(t, dryRunSigningProfile(), ghcrTarget()) }, false},
		{"signed aggregate", func(t *testing.T) *Plan { return signedTestPlan(t, cosignSigningProfile(), ghcrTarget()) }, true},
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
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, replacement string }{
		{"wrong attempt", "name: 'nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}'", "name: 'nvkr-qualification-latest'"},
		{"whole workspace", "path: .nvkr/qualification/handoff.json", "path: .nvkr"},
		{"failure upload", "if: ${{ success() }}", "if: ${{ always() }}"},
		{"no deadline", "        timeout-minutes: 5\n", ""},
		{"missing capture", " --handoff .nvkr/qualification/handoff.json", ""},
		{"hidden omission", "          include-hidden-files: 'true'\n          if-no-files-found: error\n", "          include-hidden-files: 'false'\n          if-no-files-found: error\n"},
		{"missing handoff ignored", "path: .nvkr/qualification/handoff.json\n          include-hidden-files: 'true'\n          if-no-files-found: error", "path: .nvkr/qualification/handoff.json\n          include-hidden-files: 'true'\n          if-no-files-found: ignore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := bundle.Files[GitHubWorkflowPath]
			workflow := bytes.Replace(original, []byte(test.old), []byte(test.replacement), 1)
			if bytes.Equal(workflow, original) {
				t.Fatal("mutation did not change workflow")
			}
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			changed.Files[GitHubWorkflowPath], changed.WorkflowDigest = workflow, pipeline.FileDigest(workflow)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
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
