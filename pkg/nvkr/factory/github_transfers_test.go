// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestGitHubTransfersUsePlanDerivedCommandsAndExactActions(t *testing.T) {
	for _, direction := range []string{"restore", "archive"} {
		t.Run(direction, func(t *testing.T) {
			plan := &Plan{Actions: githubTestPlan(t).Actions}
			actions, err := resolveGitHubActions(plan.Actions)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, jobs := map[string]Artifact{}, map[string]string{}
			var ids []string
			for _, name := range []string{"a", "b", "c"} {
				id := "input/" + name
				ids = append(ids, id)
				artifact := Artifact{ID: id, Name: name, Path: ".nvkr/factory/" + name, Producer: "producer/" + name}
				artifacts[id] = artifact
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
				jobs[artifact.Producer] = "job-" + name
			}
			var rendered bytes.Buffer
			var run, want string
			if direction == "restore" {
				stage := githubStage{ID: "consumer-job", Nodes: []Node{{ID: "consumer", Inputs: ids}}}
				if err := writeGitHubStageInputSteps(&rendered, plan, stage, artifacts, jobs, actions); err != nil {
					t.Fatal(err)
				}
				steps := decodeGitHubSteps(t, rendered.Bytes())
				if len(steps) != 2 || steps[0].ContinueOnError || steps[0].With["pattern"] != "{nvkr-input-a,nvkr-input-b,nvkr-input-c}" || steps[0].With["merge-multiple"] != "true" {
					t.Fatalf("stage input batch not exact and mandatory: %+v", steps)
				}
				run = steps[1].Run
				want = "./nvkr-exec factory github restore-inputs --plan " + FactoryPlanPath + " --stage consumer-job"
				if steps[1].ContinueOnError || steps[1].If != "" || len(steps[1].Env) != 0 {
					t.Fatalf("input restore must be mandatory and credential-free: %+v", steps[1])
				}
			} else {
				node := Node{ID: "package/test", Kind: NodePackage, Outputs: ids}
				writeGitHubOutputSteps(&rendered, plan, node, artifacts, actions)
				steps := decodeGitHubSteps(t, rendered.Bytes())
				if len(steps) != 4 {
					t.Fatalf("want one archive + three independent uploads, got %d", len(steps))
				}
				for _, id := range ids {
					if !actions.stepUploads(steps, githubArtifactName(id), carrierPath(id)) {
						t.Fatalf("exact output carrier %q lost", id)
					}
				}
				run = steps[0].Run
				want = "./nvkr-exec factory github pack-outputs --plan " + FactoryPlanPath + " --node package/test"
				if steps[0].ContinueOnError || steps[0].If != "" || len(steps[0].Env) != 0 {
					t.Fatalf("output packing must be mandatory and credential-free: %+v", steps[0])
				}
			}
			if strings.TrimSpace(run) != want {
				t.Fatalf("carrier helper = %q, want %q", run, want)
			}
		})
	}
}

func TestGitHubCarrierPatternRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name      string
		artifacts []Artifact
	}{
		{"empty", nil},
		{"duplicate", []Artifact{{ID: "a"}, {ID: "a"}}},
		{"lossy normalization", []Artifact{{ID: "a/b-c"}, {ID: "a-b/c"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pattern, err := githubCarrierPattern(test.artifacts)
			var coded *e.Error
			if pattern != "" || !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("ambiguous selection returned %q, %v", pattern, err)
			}
		})
	}
}

func decodeGitHubSteps(t *testing.T, raw []byte) []githubStep {
	t.Helper()
	var document struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict(append([]byte("steps:\n"), raw...), &document); err != nil {
		t.Fatal(err)
	}
	return document.Steps
}

func TestGitHubRecoveryCommandRemainsCompactForLargeGraph(t *testing.T) {
	plan := &Plan{Actions: githubTestPlan(t).Actions}
	jobs := map[string]string{}
	for i := range 150 {
		name := fmt.Sprintf("artifact-%03d", i)
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: name, Name: name, Path: ".nvkr/factory/" + name, Producer: name})
		jobs[name] = "job-" + name
	}
	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, jobs); err != nil {
		t.Fatal(err)
	}
	steps := decodeGitHubSteps(t, rendered.Bytes())
	if len(steps) != 2 {
		t.Fatalf("large recovery requires download and one helper, got %d steps", len(steps))
	}
	for _, step := range steps {
		if len(step.Run) > githubMaximumRunBytes {
			t.Fatalf("step %q has %d bytes", step.Name, len(step.Run))
		}
	}
	want := "./nvkr-exec factory github recover --plan " + FactoryPlanPath
	if strings.TrimSpace(steps[1].Run) != want {
		t.Fatalf("recovery command expanded with graph: %q", steps[1].Run)
	}
}

func TestGitHubRunScriptLimitAdmission(t *testing.T) {
	for _, size := range []int{githubMaximumRunBytes, githubMaximumRunBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			doc := githubWorkflowDocument{Jobs: map[string]githubJob{"job": {Steps: []githubStep{{Name: "oversize", Run: strings.Repeat("a", size)}}}}}
			err := validateGitHubScriptLimits(doc)
			if size == githubMaximumRunBytes {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("oversized script must fail capability admission: %v", err)
			}
		})
	}
}

func TestGitHubOversizedScriptRejectsBundleBeforeOutput(t *testing.T) {
	plan := githubTestPlan(t)
	plan.Environment.Runtime.Setup = []string{"true # " + strings.Repeat("x", githubMaximumRunBytes)}
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	var coded *e.Error
	if bundle != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("oversized script returned bundle=%v, err=%v", bundle != nil, err)
	}
}

func TestGitHubBatchSelectionCannotExpandRequiredClosure(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	var pattern string
	for _, id := range sortedKeys(document.Jobs) {
		if id == "nvkr-lifecycle-run" {
			continue
		}
		for _, step := range document.Jobs[id].Steps {
			if step.With["pattern"] != "" {
				pattern = step.With["pattern"]
				step.With["pattern"] = "*"
				break
			}
		}
		if pattern != "" {
			break
		}
	}
	if pattern == "" {
		t.Fatal("test plan has no external input batch")
	}
	raw, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	mutated := relowerGitHubTestBundle(t, plan, bundle, raw)
	var coded *e.Error
	if err := ValidateGitHubBundle(t.Context(), plan, mutated); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("broadened download set was admitted: %v", err)
	}
}

func TestGitHubInputRestoreCommandRemainsCompactForLargeGraph(t *testing.T) {
	for _, count := range []int{1, 34, 150} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			plan := &Plan{Actions: githubTestPlan(t).Actions}
			actions, err := resolveGitHubActions(plan.Actions)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, jobs := map[string]Artifact{}, map[string]string{}
			var inputs []string
			for i := range count {
				id := fmt.Sprintf("result/cwe/linux/arm64/component-%03d", i)
				inputs = append(inputs, id)
				artifact := Artifact{ID: id, Name: id, Path: ".nvkr/factory/" + id, Producer: "producer/" + id}
				artifacts[id], jobs[artifact.Producer] = artifact, "job-"+id
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
			}
			stage := githubStage{ID: "consumer", Nodes: []Node{{ID: "consumer", Inputs: inputs}}}
			var rendered bytes.Buffer
			if err := writeGitHubStageInputSteps(&rendered, plan, stage, artifacts, jobs, actions); err != nil {
				t.Fatal(err)
			}
			steps := decodeGitHubSteps(t, rendered.Bytes())
			if len(steps) != 2 {
				t.Fatalf("restore requires download and one helper, got %d steps", len(steps))
			}
			for _, step := range steps {
				if step.ContinueOnError || len(step.Run) > githubMaximumRunBytes {
					t.Fatalf("input restore is tolerant or oversized: %s", step.Name)
				}
			}
			want := "./nvkr-exec factory github restore-inputs --plan " + FactoryPlanPath + " --stage consumer"
			if strings.TrimSpace(steps[1].Run) != want {
				t.Fatalf("restore command expanded with graph: %q", steps[1].Run)
			}
			for _, id := range inputs {
				if !stepDownloadsArtifact(plan, steps, githubArtifactName(id)) {
					t.Fatalf("download omitted %s", id)
				}
			}
		})
	}
}
