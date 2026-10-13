// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestGitHubFinalizerDownloadsExactCarriersAndDelegatesRecovery(t *testing.T) {
	plan := githubTestPlan(t)
	nodeJobs := make(map[string]string, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodeJobs[node.ID] = githubJobID(node.ID)
	}

	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, nodeJobs); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict(append([]byte("steps:\n"), rendered.Bytes()...), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Steps) != 2 {
		t.Fatalf("finalizer steps = %d, want download and recovery helper", len(document.Steps))
	}
	download := document.Steps[0]
	if download.Uses != plan.Actions["download-artifact"].Uses() || !download.ContinueOnError ||
		download.With["path"] != ".nvkr/carriers" || download.With["merge-multiple"] != "true" {
		t.Fatalf("batch download is incomplete: %+v", download)
	}
	wantNames := make([]string, len(plan.Graph.Artifacts))
	for index, artifact := range plan.Graph.Artifacts {
		wantNames[index] = githubArtifactName(artifact.ID)
	}
	sort.Strings(wantNames)
	wantPattern := wantNames[0]
	if len(wantNames) > 1 {
		wantPattern = "{" + strings.Join(wantNames, ",") + "}"
	}
	if download.With["pattern"] != wantPattern || strings.Contains(download.With["pattern"], "nvkr-exec") ||
		strings.Contains(download.With["pattern"], "nvkr-lifecycle-run") {
		t.Fatalf("batch pattern = %q, want exact carriers %q", download.With["pattern"], wantPattern)
	}
	recovery := document.Steps[1]
	want := "./nvkr-exec factory github recover --plan " + FactoryPlanPath
	if strings.TrimSpace(recovery.Run) != want || recovery.ID != "recover-artifacts" || recovery.ContinueOnError || recovery.If != "" || recovery.Uses != "" {
		t.Fatalf("recovery helper is not exact and mandatory: %+v", recovery)
	}
	if len(recovery.Env) != 1 || recovery.Env["NVKR_JOB_RESULTS"] != "${{ toJSON(needs) }}" {
		t.Fatalf("recovery does not receive only provider job descriptions: %+v", recovery.Env)
	}
}

func TestGitHubFinalizerRejectsMergedCarrierNameCollisionsBeforeOutput(t *testing.T) {
	plan := &Plan{Actions: githubTestPlan(t).Actions, Graph: ArtifactGraph{Artifacts: []Artifact{
		{ID: "result/a-b", Producer: "producer-a", Path: ".nvkr/factory/a"},
		{ID: "result/a/b", Producer: "producer-b", Path: ".nvkr/factory/b"},
	}}}
	var rendered bytes.Buffer
	err := writeGitHubFinalizerRestoreSteps(&rendered, plan, map[string]string{
		"producer-a": "producer-a", "producer-b": "producer-b",
	})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog || rendered.Len() != 0 {
		t.Fatalf("colliding carrier names returned bytes=%d error=%v", rendered.Len(), err)
	}
}

func TestGitHubDiskBuildFinalizerUsesSelectedNativePool(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, buildOnly := range []bool{false, true} {
			name := platform + "/publication"
			if buildOnly {
				name = platform + "/build-only"
			}
			t.Run(name, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				if buildOnly {
					plan = githubDiskBuildPlan(t, platform, "luks2")
				}
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				job := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath]).Jobs["nvkr-lifecycle-run"]
				wantRunner, wantTimeout, wantEnvironment := githubBootstrapRunner(githubARCRunner(platform)), githubSupportJobTimeoutMinutes, ""
				if buildOnly {
					wantRunner, wantTimeout, wantEnvironment = githubARCRunner(platform), githubDiskBuildFinalizerTimeoutMinutes, githubARCEnvironment
					if len(job.Permissions) != 1 || job.Permissions["contents"] != "read" || len(job.Env) != 0 {
						t.Fatalf("build-only finalizer has unnecessary permissions/environment: %+v", job)
					}
				}
				if job.RunsOn != wantRunner || job.TimeoutMinutes != wantTimeout || job.Environment != wantEnvironment || job.If != "${{ always() && "+githubDispatchCondition(plan)+" }}" {
					t.Fatalf("finalizer placement differs: runner=%s timeout=%d environment=%s if=%s", job.RunsOn, job.TimeoutMinutes, job.Environment, job.If)
				}
				if !stepDownloadsArtifact(plan, job.Steps, "nvkr-exec-"+shortStableName(githubARCRunner(platform))) {
					t.Fatal("missing native helper")
				}
			})
		}
	}
}

func TestGitHubDiskBuildFinalizerRejectsAmbiguousClosure(t *testing.T) {
	for _, mutation := range []string{"missing output", "multiple outputs", "non-disk output", "multiple builds"} {
		t.Run(mutation, func(t *testing.T) {
			plan := githubDiskBuildPlan(t, "linux/amd64", "none")
			switch mutation {
			case "missing output":
				plan.LifecycleOutputs = nil
			case "multiple outputs":
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, plan.LifecycleOutputs[0])
			case "non-disk output":
				plan.LifecycleOutputs[0].Kind = PackageBinaryBundle
			case "multiple builds":
				plan.Builds = append(plan.Builds, plan.Builds[0])
			}
			runner, err := githubDiskBuildFinalizerRunner(plan)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if runner != "" {
				t.Fatalf("ambiguous closure selected runner %s", runner)
			}
		})
	}
}
