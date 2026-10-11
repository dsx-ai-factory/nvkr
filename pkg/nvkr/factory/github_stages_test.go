// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

func TestGitHubStageTimeoutMinutes(t *testing.T) {
	requirements := func(seconds uint64) *execution.Requirements {
		return &execution.Requirements{TimeoutSeconds: seconds}
	}
	tests := []struct {
		name    string
		stage   githubStage
		want    int
		wantErr e.Code
	}{
		{name: "adapter default", stage: githubStage{ID: "default", Nodes: []Node{{ID: "build"}}}, want: 60},
		{name: "round one second up", stage: githubStage{ID: "short", Nodes: []Node{{ID: "build", Requirements: requirements(1)}}}, want: 1},
		{name: "round partial minute up", stage: githubStage{ID: "partial", Nodes: []Node{{ID: "build", Requirements: requirements(61)}}}, want: 2},
		{name: "NVKE image budget", stage: githubStage{ID: "nvke-image", Nodes: []Node{{ID: "build", Requirements: requirements(21600)}}}, want: 360},
		{
			name: "sum sequential nodes",
			stage: githubStage{ID: "grouped", Nodes: []Node{
				{ID: "first", Requirements: requirements(60)},
				{ID: "second", Requirements: requirements(61)},
			}},
			want: 3,
		},
		{
			name: "sum declared and default sequential nodes",
			stage: githubStage{ID: "mixed", Nodes: []Node{
				{ID: "declared", Requirements: requirements(60)},
				{ID: "undeclared"},
			}},
			want: 61,
		},
		{name: "reject over GitHub limit", stage: githubStage{ID: "too-long", Nodes: []Node{{ID: "build", Requirements: requirements(21601)}}}, wantErr: e.CodeEngineCapability},
		{name: "disk requires derived budget", stage: githubStage{ID: "disk", DiskOutput: "image", Nodes: []Node{{Requirements: requirements(86400)}}}, wantErr: e.CodeEngineCapability},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := githubStageTimeoutMinutes(test.stage)
			if test.wantErr != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.wantErr {
					t.Fatalf("want %s, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %d minutes, want %d", got, test.want)
			}
		})
	}
}

func TestGitHubContractedStagesRejectIntroducedCycle(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "acyclic", true: "contracted cycle"}[cycle], func(t *testing.T) {
			plan := &Plan{Graph: ArtifactGraph{Artifacts: []Artifact{{ID: "a", Producer: "a1"}, {ID: "b", Producer: "b"}}}}
			stages := []githubStage{{ID: "disk", Nodes: []Node{{ID: "a1"}, {ID: "a2"}}}, {ID: "utility", Nodes: []Node{{ID: "b", Inputs: []string{"a"}}}}}
			if cycle {
				stages[0].Nodes[1].Inputs = []string{"b"}
			}
			err := validateGitHubStageDAG(plan, stages, map[string]string{"a1": "disk", "a2": "disk", "b": "utility"})
			if cycle {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubStagesGroupOnlyCompatibleOfflineUtilityNodes(t *testing.T) {
	base := Node{
		ID:        "observe",
		Kind:      NodeVerify,
		RunsOn:    "ubuntu-latest",
		Network:   ir.NetworkNone,
		Container: "utility@sha256:" + strings.Repeat("1", 64),
		Platform:  "linux/amd64",
	}
	tests := []struct {
		name   string
		mutate func(*Node, *Node)
		want   int
	}{
		{name: "compatible", want: 1},
		{name: "network boundary", mutate: func(_, second *Node) { second.Network = ir.NetworkDefault }, want: 2},
		{name: "container boundary", mutate: func(_, second *Node) { second.Container = "other@sha256:" + strings.Repeat("2", 64) }, want: 2},
		{name: "platform boundary", mutate: func(_, second *Node) { second.Platform = "linux/arm64" }, want: 2},
		{name: "runner boundary", mutate: func(_, second *Node) { second.RunsOn = "ubuntu-24.04-arm" }, want: 2},
		{name: "build boundary", mutate: func(first, _ *Node) { first.Kind = NodeBuild }, want: 2},
		{name: "fetch boundary", mutate: func(_, second *Node) { second.Kind = NodeFetch }, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := base
			second := base
			second.ID = "package"
			second.Kind = NodePackage
			if test.mutate != nil {
				test.mutate(&first, &second)
			}
			stages, jobs, err := githubStages(&Plan{Graph: ArtifactGraph{Nodes: []Node{first, second}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(stages) != test.want {
				t.Fatalf("got %d stages, want %d: %#v", len(stages), test.want, stages)
			}
			if (jobs[first.ID] == jobs[second.ID]) != (test.want == 1) {
				t.Fatalf("node-to-stage mapping does not match grouping: %v", jobs)
			}
		})
	}
}

func TestRenderGitHubGroupedStagePreservesExecutionAndTransport(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := requireGroupedGitHubStage(t, stages)
	job := document.Jobs[stage.ID]

	var executed []string
	for _, step := range job.Steps {
		if step.ID != "" {
			executed = append(executed, step.ID)
		}
	}
	wantExecuted := make([]string, 0, len(stage.Nodes))
	for _, node := range stage.Nodes {
		wantExecuted = append(wantExecuted, githubJobID(node.ID))
		for _, output := range node.Outputs {
			if !testActionRefs(t, plan).stepUploads(job.Steps, githubArtifactName(output), carrierPath(output)) {
				t.Errorf("logical output %q is not uploaded from grouped stage %q", output, stage.ID)
			}
		}
	}
	if strings.Join(executed, "\x00") != strings.Join(wantExecuted, "\x00") {
		t.Fatalf("execution order %v, want %v", executed, wantExecuted)
	}

	internalInputs := 0
	externalInputs := 0
	wantRestore := "./nvkr-exec factory github restore-inputs --plan " + FactoryPlanPath + " --stage " + shellQuote(stage.ID)
	restores := 0
	for _, step := range job.Steps {
		if strings.TrimSpace(step.Run) == wantRestore {
			restores++
		}
	}
	if restores != 1 {
		t.Fatalf("grouped stage has %d exact input-restore helpers, want one", restores)
	}
	for _, node := range stage.Nodes {
		for _, input := range node.Inputs {
			internal := nodeJobs[artifacts[input].Producer] == stage.ID
			downloaded := stepDownloadsArtifact(plan, job.Steps, githubArtifactName(input))
			if internal && downloaded {
				t.Errorf("internal input %q was redundantly downloaded", input)
			}
			if !internal && !downloaded {
				t.Errorf("external input %q was not downloaded", input)
			}
			if internal {
				internalInputs++
			} else {
				externalInputs++
			}
		}
	}
	if internalInputs == 0 || externalInputs == 0 {
		t.Fatalf("fixture must exercise both internal and external transport; got %d internal, %d external inputs", internalInputs, externalInputs)
	}
}

func TestValidateGitHubBundleRejectsGroupedExecutionTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]githubStep, []int) []githubStep
	}{
		{
			name: "omitted logical node",
			mutate: func(steps []githubStep, execute []int) []githubStep {
				index := execute[0]
				return append(steps[:index:index], steps[index+1:]...)
			},
		},
		{
			name: "reordered logical nodes",
			mutate: func(steps []githubStep, execute []int) []githubStep {
				steps[execute[0]], steps[execute[1]] = steps[execute[1]], steps[execute[0]]
				return steps
			},
		},
		{
			name: "continue after node failure",
			mutate: func(steps []githubStep, execute []int) []githubStep {
				steps[execute[0]].ContinueOnError = true
				return steps
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			bundle, err := RenderGitHub(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			stage := requireGroupedGitHubStage(t, stages)
			job := document.Jobs[stage.ID]
			var execute []int
			for index, step := range job.Steps {
				if step.ID != "" {
					execute = append(execute, index)
				}
			}
			if len(execute) < 2 {
				t.Fatalf("grouped stage %q has only %d execution steps", stage.ID, len(execute))
			}
			job.Steps = test.mutate(job.Steps, execute)
			document.Jobs[stage.ID] = job
			workflow, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			workflow = append([]byte("# qualification: "+plan.Qualification.String()+"\n# planDigest: "+bundle.PlanDigest+"\n"), workflow...)
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = append([]byte(nil), raw...)
			}
			mutated.Files[GitHubWorkflowPath] = workflow
			mutated.WorkflowDigest = pipeline.FileDigest(workflow)
			err = ValidateGitHubBundle(context.Background(), plan, &mutated)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("want %s, got %v", e.CodeEngineCapability, err)
			}
		})
	}
}

func TestValidateGitHubBundleRejectsUnrecognizedWorkflowAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "appended bootstrap shell",
			mutate: func(raw []byte) []byte {
				return bytes.Replace(raw, []byte(" --root . --check\n"), []byte(" --root . --check && printf injected\n"), 1)
			},
		},
		{
			name: "injected stage step",
			mutate: func(raw []byte) []byte {
				return bytes.Replace(raw, []byte("      - name: 'Fetch pinned source"), []byte("      - name: Injected shell\n        run: printf injected\n      - name: 'Fetch pinned source"), 1)
			},
		},
		{
			name: "protected environment on bootstrap",
			mutate: func(raw []byte) []byte {
				return bytes.Replace(raw, []byte("    timeout-minutes: 15\n    steps:\n"), []byte("    timeout-minutes: 15\n    environment: 'nkd-release'\n    steps:\n"), 1)
			},
		},
		{
			name: "protected environment on finalizer",
			mutate: func(raw []byte) []byte {
				return bytes.Replace(raw, []byte("    timeout-minutes: 15\n    needs: ["), []byte("    timeout-minutes: 15\n    environment: 'nkd-release'\n    needs: ["), 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := githubLifecycleTestPlan(t)
			bundle, err := RenderGitHub(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			original := bundle.Files[GitHubWorkflowPath]
			workflow := test.mutate(original)
			if bytes.Equal(workflow, original) {
				t.Fatal("mutation changed no workflow bytes")
			}
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = append([]byte(nil), raw...)
			}
			mutated.Files[GitHubWorkflowPath] = workflow
			mutated.WorkflowDigest = pipeline.FileDigest(workflow)
			err = ValidateGitHubBundle(context.Background(), plan, &mutated)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("want %s, got %v", e.CodeEngineCapability, err)
			}
		})
	}
}

func decodeGitHubWorkflow(t *testing.T, raw []byte) githubWorkflowDocument {
	t.Helper()
	var document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func requireGroupedGitHubStage(t *testing.T, stages []githubStage) githubStage {
	t.Helper()
	for _, stage := range stages {
		if len(stage.Nodes) > 1 {
			return stage
		}
	}
	t.Fatal("test plan contains no grouped GitHub stage")
	return githubStage{}
}

func stepDownloadsArtifact(plan *Plan, steps []githubStep, name string) bool {
	for _, step := range steps {
		if step.Uses != plan.Actions["download-artifact"].Uses() {
			continue
		}
		if step.With["name"] == name || step.With["pattern"] == name {
			return true
		}
		for _, member := range strings.Split(strings.Trim(step.With["pattern"], "{}"), ",") {
			if member == name {
				return true
			}
		}
	}
	return false
}

func githubLifecycleTestPlan(t *testing.T) *Plan {
	t.Helper()
	// Compilation returns complete public identities and output bindings.
	return githubTestPlan(t)
}
