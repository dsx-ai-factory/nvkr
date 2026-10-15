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

func TestGitHubStagesGroupOnlyTargetObservationAndSBOM(t *testing.T) {
	base := Node{
		ID:          "observe",
		Kind:        NodeVerify,
		BuildTarget: "worker",
		RunsOn:      "ubuntu-latest",
		Network:     ir.NetworkNone,
		Container:   "utility@sha256:" + strings.Repeat("1", 64),
		Platform:    "linux/amd64",
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
		{name: "package boundary", mutate: func(_, second *Node) { second.Kind = NodePackage }, want: 2},
		{name: "different target", mutate: func(_, second *Node) { second.BuildTarget = "other" }, want: 2},
		{name: "no target", mutate: func(first, second *Node) { first.BuildTarget, second.BuildTarget = "", "" }, want: 2},
		{name: "no observation dependency", mutate: func(_, second *Node) { second.Inputs = nil }, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := base
			first.Outputs = []string{"observation"}
			second := base
			second.ID = "sbom"
			second.Kind = NodeSBOM
			second.Inputs = []string{"observation"}
			if test.mutate != nil {
				test.mutate(&first, &second)
			}
			stages, jobs, err := githubStages(&Plan{Graph: ArtifactGraph{Nodes: []Node{first, second},
				Artifacts: []Artifact{{ID: "observation", Producer: first.ID}}}})
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

func TestGitHubObservationPairIgnoresUnrelatedInterleaving(t *testing.T) {
	for _, extraInput := range []bool{false, true} {
		name := "independent insertion"
		if extraInput {
			name = "new readiness dependency"
		}
		t.Run(name, func(t *testing.T) {
			observe := Node{ID: "a-observe", Kind: NodeVerify, BuildTarget: "worker", Network: ir.NetworkNone,
				Container: "utility@sha256:" + strings.Repeat("1", 64), RunsOn: "ubuntu-24.04", Platform: "linux/amd64",
				Outputs: []string{"observation"}}
			sbom := observe
			sbom.ID, sbom.Kind, sbom.Outputs = "z-sbom", NodeSBOM, nil
			sbom.Inputs = []string{"observation"}
			if extraInput {
				sbom.Inputs = append(sbom.Inputs, "unrelated")
			}
			plan := &Plan{Graph: ArtifactGraph{
				Nodes:     []Node{observe, {ID: "middle", Kind: NodeAcquire, Outputs: []string{"unrelated"}}, sbom},
				Artifacts: []Artifact{{ID: "observation", Producer: observe.ID}, {ID: "unrelated", Producer: "middle"}},
			}}
			stages, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			if (jobs[observe.ID] == jobs[sbom.ID]) == extraInput {
				t.Fatalf("grouping must ignore unrelated work but retain new readiness boundaries: %v", jobs)
			}
			if jobs["middle"] == jobs[observe.ID] || jobs["middle"] == jobs[sbom.ID] {
				t.Fatal("unrelated work entered observation stage")
			}
			if !extraInput && (len(stages) != 2 || stages[0].Nodes[0].ID != observe.ID || stages[0].Nodes[1].ID != sbom.ID) {
				t.Fatal("pair lost exact observation-before-SBOM execution order")
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
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
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
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := requireGroupedGitHubStage(t, stages)
	tests := []struct {
		name     string
		unshared bool
		mutate   func([]githubStep, []int) []githubStep
	}{
		{
			name: "omitted logical node",
			mutate: func(steps []githubStep, execute []int) []githubStep {
				index := execute[0]
				return append(steps[:index:index], steps[index+1:]...)
			},
		},
		{
			name:     "reordered logical nodes",
			unshared: true,
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
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
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
			lower := relowerGitHubTestBundle
			if test.unshared {
				// The mismatched execution/collection pair is intentionally
				// outside the shared interface so the validator owns rejection.
				lower = unsharedGitHubTestBundle
			}
			mutated := lower(t, plan, bundle, workflow)
			err = ValidateGitHubBundle(context.Background(), plan, mutated)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("want %s, got %v", e.CodeEngineCapability, err)
			}
		})
	}
}

func TestValidateGitHubBundleRejectsUnrecognizedWorkflowAuthority(t *testing.T) {
	plan := githubLifecycleTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		entrypoint bool
		mutate     func(*githubWorkflowDocument) bool
	}{
		{
			name: "appended bootstrap shell",
			mutate: func(document *githubWorkflowDocument) bool {
				for _, id := range sortedKeys(document.Jobs) {
					job := document.Jobs[id]
					for index, step := range job.Steps {
						if strings.Contains(step.Run, " --root . --check") {
							job.Steps[index].Run = strings.TrimSpace(step.Run) + " && printf injected\n"
							document.Jobs[id] = job
							return true
						}
					}
				}
				return false
			},
		},
		{
			name:       "injected stage step",
			entrypoint: true,
			mutate: func(document *githubWorkflowDocument) bool {
				for _, id := range sortedKeys(document.Jobs) {
					job := document.Jobs[id]
					for index, step := range job.Steps {
						if step.Uses == "./"+githubSharedDirectory+"execute-node" || step.Uses == "./"+githubSharedDirectory+"execute-source" {
							steps := append([]githubStep(nil), job.Steps[:index]...)
							steps = append(steps, githubStep{Name: "Injected shell", Run: "printf injected"})
							job.Steps = append(steps, job.Steps[index:]...)
							document.Jobs[id] = job
							return true
						}
					}
				}
				return false
			},
		},
		{
			name: "protected environment on bootstrap",
			mutate: func(document *githubWorkflowDocument) bool {
				for _, id := range sortedKeys(document.Jobs) {
					if strings.HasPrefix(id, "build-runner-") {
						job := document.Jobs[id]
						job.Environment = githubReleaseEnvironment
						document.Jobs[id] = job
						return true
					}
				}
				return false
			},
		},
		{
			name: "protected environment on finalizer",
			mutate: func(document *githubWorkflowDocument) bool {
				job, ok := document.Jobs["nvkr-lifecycle-run"]
				job.Environment = githubReleaseEnvironment
				document.Jobs["nvkr-lifecycle-run"] = job
				return ok
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := expandedGitHubWorkflow(t, bundle)
			if test.entrypoint {
				// Inject an unknown operation beside a real shared action call.
				// Retaining the member set exercises executable-byte validation.
				raw = bundle.Files[GitHubWorkflowPath]
			}
			document := decodeGitHubWorkflow(t, raw)
			baseline, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if !test.mutate(&document) {
				t.Fatal("mutation did not find the intended workflow authority")
			}
			workflow, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(workflow, baseline) {
				t.Fatal("mutation did not change workflow semantics")
			}
			var mutated *GeneratedBundle
			if test.entrypoint {
				header := "# qualification: " + plan.Qualification.String() + "\n# plan: " + bundle.PlanPath + "\n# manifest: " + bundle.ManifestPath + "\n"
				workflow = append([]byte(header), workflow...)
				mutated = mutateGitHubBundleFile(t, bundle, bundle.WorkflowPath, func([]byte) []byte { return workflow })
			} else {
				mutated = relowerGitHubTestBundle(t, plan, bundle, workflow)
			}
			err = ValidateGitHubBundle(context.Background(), plan, mutated)
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
