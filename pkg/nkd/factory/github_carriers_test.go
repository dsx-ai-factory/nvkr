// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestGitHubCarriersRestoreOnlyExternalStageInputs(t *testing.T) {
	plan := githubTestPlan(t)
	stage, inputs, artifacts := carrierTestStage(t, plan)
	producer, consumer := t.TempDir(), t.TempDir()
	for _, id := range inputs {
		carrierTestArchive(t, producer, consumer, artifacts[id], id)
	}
	var internal Artifact
	for _, node := range stage.Nodes {
		for _, input := range node.Inputs {
			if !containsString(inputs, input) {
				internal = artifacts[input]
			}
		}
	}
	if internal.ID == "" {
		t.Fatal("fixture needs an internal grouped-stage edge")
	}
	// A matching provider download may also contain an unneeded carrier; the
	// helper must select only external edges, never every file in the directory.
	carrierTestArchive(t, producer, consumer, internal, "unrequested")
	t.Chdir(consumer)
	var output bytes.Buffer
	summary := filepath.Join(consumer, "summary.md")
	if err := RestoreGitHubInputs(context.Background(), plan, stage.ID, GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: summary}); err != nil {
		t.Fatal(err)
	}
	for _, id := range inputs {
		raw, err := os.ReadFile(artifacts[id].Path)
		if err != nil || string(raw) != id {
			t.Fatalf("input %s: bytes=%q error=%v", id, raw, err)
		}
		if _, err := os.Stat(carrierPath(id)); !os.IsNotExist(err) {
			t.Fatalf("restored carrier retained duplicate storage: %v", err)
		}
	}
	if _, err := os.Stat(internal.Path); !os.IsNotExist(err) {
		t.Fatalf("stage-internal input restored: %v", err)
	}
	if _, err := os.Stat(carrierPath(internal.ID)); err != nil {
		t.Fatalf("unrequested carrier removed: %v", err)
	}
	if !strings.Contains(output.String(), "Completed: Restore stage inputs") {
		t.Fatalf("missing completion: %s", output.String())
	}
}

func TestGitHubCarriersRestoreFailsAtExactInput(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt-%t", corrupt), func(t *testing.T) {
			plan := githubTestPlan(t)
			stage, inputs, artifacts := carrierTestStage(t, plan)
			producer, consumer := t.TempDir(), t.TempDir()
			for _, id := range inputs {
				if id != inputs[1] {
					carrierTestArchive(t, producer, consumer, artifacts[id], id)
				}
			}
			if corrupt {
				carrierTestWrite(t, filepath.Join(consumer, carrierPath(inputs[1])), "not a tar")
			}
			t.Chdir(consumer)
			var output bytes.Buffer
			err := RestoreGitHubInputs(context.Background(), plan, stage.ID, GitHubStepOptions{Stdout: &output, Stderr: &output})
			if factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("restore error=%v", err)
			}
			if _, err := os.Stat(artifacts[inputs[0]].Path); err != nil {
				t.Fatal("first input was not restored", err)
			}
			if _, err := os.Stat(artifacts[inputs[2]].Path); !os.IsNotExist(err) {
				t.Fatalf("restore continued after failure: %v", err)
			}
			if !strings.Contains(output.String(), "at restore "+inputs[1]) {
				t.Fatalf("failed input not reported: %s", output.String())
			}
		})
	}
}

func TestGitHubCarriersPackExactOutputs(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-%t", missing), func(t *testing.T) {
			plan := githubTestPlan(t)
			_, artifacts := planIndex(plan)
			var node Node
			for _, candidate := range plan.Graph.Nodes {
				if len(candidate.Outputs) >= 2 {
					node = candidate
					break
				}
			}
			if node.ID == "" {
				t.Fatal("fixture needs a multi-output producer")
			}
			outputs := uniqueSorted(node.Outputs)
			t.Chdir(t.TempDir())
			for _, id := range outputs {
				if missing && id == outputs[0] {
					continue
				}
				carrierTestWrite(t, artifacts[id].Path, id)
			}
			var output bytes.Buffer
			err := PackGitHubOutputs(context.Background(), plan, node.ID, GitHubStepOptions{Stdout: &output, Stderr: &output})
			if missing {
				if factoryErrorCode(err) != e.CodeReleaseIncomplete {
					t.Fatalf("pack error=%v", err)
				}
				if _, err := os.Stat(carrierPath(outputs[1])); !os.IsNotExist(err) {
					t.Fatalf("pack continued after failure: %v", err)
				}
				if !strings.Contains(output.String(), "at archive "+outputs[0]) {
					t.Fatalf("missing failure location: %s", output.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			for _, id := range outputs {
				if err := transportarchive.Unpack(context.Background(), carrierPath(id), destination, []string{artifacts[id].Path}); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(destination, artifacts[id].Path))
				if err != nil || string(raw) != id {
					t.Fatalf("packed output %s: bytes=%q error=%v", id, raw, err)
				}
			}
			entries, err := os.ReadDir(".nkd/carriers")
			if err != nil || len(entries) != len(outputs) {
				t.Fatalf("packed unexpected carriers: count=%d error=%v", len(entries), err)
			}
		})
	}
}

func TestGitHubCarriersRecoveryRecordsAllOutcomes(t *testing.T) {
	plan := githubTestPlan(t)
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := append([]Artifact(nil), plan.Graph.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	failedJob := nodeJobs[artifacts[0].Producer]
	results := carrierTestResults(t, plan)
	results[failedJob] = map[string]any{"result": "failure", "outputs": map[string]string{"secret": "MUST-NOT-BE-LOGGED"}}
	producer, consumer := t.TempDir(), t.TempDir()
	want := map[string]string{}
	count := 0
	for _, artifact := range artifacts {
		if nodeJobs[artifact.Producer] == failedJob {
			want[artifact.ID] = "job-failed"
			// Even an available carrier from a failed job is not restored.
			carrierTestArchive(t, producer, consumer, artifact, artifact.ID)
			continue
		}
		switch count {
		case 0:
			want[artifact.ID] = "restore-failed"
			carrierTestWrite(t, filepath.Join(consumer, carrierPath(artifact.ID)), "invalid tar")
		case 1:
			want[artifact.ID] = "restored:success"
			carrierTestArchive(t, producer, consumer, artifact, artifact.ID)
		default:
			want[artifact.ID] = "missing"
		}
		count++
	}
	if count < 3 {
		t.Fatal("fixture does not cover all restore outcomes")
	}
	t.Chdir(consumer)
	var output bytes.Buffer
	summary := filepath.Join(consumer, "summary.md")
	if err := RecoverGitHubArtifacts(context.Background(), plan, carrierTestJSON(t, results), GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: summary}); err != nil {
		t.Fatal(err)
	}
	summaryBytes, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		status := want[artifact.ID]
		raw, err := os.ReadFile(HostedRestoreOutcomePath(".nkd/factory", artifact.ID))
		if err != nil || string(raw) != status+"\n" {
			t.Fatalf("%s outcome=%q error=%v want=%s", artifact.ID, raw, err, status)
		}
		if !strings.Contains(string(summaryBytes), githubSummaryText("Recovery: "+artifact.ID+" — "+status)) {
			t.Fatalf("summary omitted %s: %s", artifact.ID, summaryBytes)
		}
		if status == "restored:success" {
			raw, err := os.ReadFile(artifact.Path)
			if err != nil || string(raw) != artifact.ID {
				t.Fatalf("restored artifact=%q error=%v", raw, err)
			}
		}
	}
	if strings.Contains(output.String()+string(summaryBytes), "MUST-NOT-BE-LOGGED") {
		t.Fatal("provider outputs leaked into diagnostics")
	}
	if err := ValidateHostedRestores(context.Background(), ".nkd/factory", plan); factoryErrorCode(err) != e.CodeProvenanceMissing {
		t.Fatalf("diagnostic recovery authorized an incomplete Run: %v", err)
	}
}

func TestGitHubCarriersRejectBeforeWork(t *testing.T) {
	base := githubTestPlan(t)
	type rejection struct {
		name string
		ctx  context.Context
		plan *Plan
		code e.Code
	}
	cases := []rejection{
		{"nil-context", nil, base, e.CodeUsage},
		{"nil-plan", context.Background(), nil, e.CodeUsage},
		{"incomplete-plan", context.Background(), &Plan{}, e.CodeInvalidCatalog},
		{"unknown-selection", context.Background(), base, e.CodeInvalidCatalog},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases = append(cases, rejection{"canceled", canceled, base, e.CodeTimeout})
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	vmPlan, err := Compile(context.Background(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, rejection{"unsupported-requirements", context.Background(), vmPlan, e.CodeEngineCapability})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var output bytes.Buffer
			options := GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: "summary.md"}
			operations := []func() error{
				func() error { return RestoreGitHubInputs(test.ctx, test.plan, "absent", options) },
				func() error { return PackGitHubOutputs(test.ctx, test.plan, "absent", options) },
			}
			if test.name != "unknown-selection" {
				operations = append(operations, func() error { return RecoverGitHubArtifacts(test.ctx, test.plan, "{}", options) })
			}
			for _, operation := range operations {
				if err := operation(); factoryErrorCode(err) != test.code {
					t.Fatalf("error=%v want=%s", err, test.code)
				}
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 || output.Len() != 0 {
				t.Fatalf("rejection performed work: entries=%v error=%v output=%s", entries, err, output.String())
			}
		})
	}
}

func TestGitHubCarriersRejectCollidingCompletePlan(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	base.Components["b-containerd"] = base.Components["containerd"]
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("collision", "unused", "ubuntu-24.04")
	definition.Outputs = []authored.DefinitionOutput{
		{Name: "aa", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"b-containerd"}},
		{Name: "aa-b", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd"}},
	}
	plan, err := Compile(context.Background(), definition, environment, stack, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = "definition.yaml", "environment.yaml"
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var output bytes.Buffer
	options := GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: "summary.md"}
	for _, operation := range []func() error{
		func() error { return RestoreGitHubInputs(context.Background(), plan, "absent", options) },
		func() error { return PackGitHubOutputs(context.Background(), plan, plan.Graph.Nodes[0].ID, options) },
		func() error { return RecoverGitHubArtifacts(context.Background(), plan, "{}", options) },
	} {
		err := operation()
		if factoryErrorCode(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), "collide") {
			t.Fatalf("collision error=%v", err)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 || output.Len() != 0 {
		t.Fatalf("collision rejection performed work: %v %v %s", entries, err, output.String())
	}
}

func TestGitHubCarriersRecoveryCancellationAndStorage(t *testing.T) {
	plan := githubTestPlan(t)
	results := carrierTestJSON(t, carrierTestResults(t, plan))
	for _, mode := range []string{"cancel", "storage", "parent-symlink", "closed-diagnostics", "status-symlink"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := GitHubStepOptions{SummaryPath: "summary.md"}
			var target string
			switch mode {
			case "cancel":
				options.Stdout = carrierCancelWriter{cancel: cancel}
			case "storage":
				carrierTestWrite(t, ".nkd/restore-outcomes", "obstruction")
			case "parent-symlink":
				target = t.TempDir()
				if err := os.MkdirAll(".nkd", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, ".nkd/restore-outcomes"); err != nil {
					t.Fatal(err)
				}
			case "closed-diagnostics":
				options.Stdout, options.Stderr = carrierBrokenWriter{}, carrierBrokenWriter{}
				options.SummaryPath = "."
			case "status-symlink":
				target = filepath.Join(t.TempDir(), "unrelated")
				carrierTestWrite(t, target, "preserve")
				path := HostedRestoreOutcomePath(".nkd/factory", plan.Graph.Artifacts[0].ID)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			err := RecoverGitHubArtifacts(ctx, plan, results, options)
			switch mode {
			case "cancel":
				if factoryErrorCode(err) != e.CodeTimeout {
					t.Fatalf("cancellation=%v", err)
				}
				if _, err := os.Stat(".nkd/restore-outcomes"); !os.IsNotExist(err) {
					t.Fatalf("canceled recovery wrote outcomes: %v", err)
				}
			case "storage", "parent-symlink":
				if factoryErrorCode(err) != e.CodeInternal {
					t.Fatalf("storage error=%v", err)
				}
				if mode == "parent-symlink" {
					entries, err := os.ReadDir(target)
					if err != nil || len(entries) != 0 {
						t.Fatalf("recovery escaped outcome directory: %v %v", entries, err)
					}
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				for _, artifact := range plan.Graph.Artifacts {
					raw, err := os.ReadFile(HostedRestoreOutcomePath(".nkd/factory", artifact.ID))
					if err != nil || string(raw) != "missing\n" {
						t.Fatalf("outcome=%q error=%v", raw, err)
					}
				}
				if target != "" {
					raw, err := os.ReadFile(target)
					if err != nil || string(raw) != "preserve" {
						t.Fatalf("symlink target changed: %q %v", raw, err)
					}
				}
			}
		})
	}
}

func carrierTestStage(t *testing.T, plan *Plan) (githubStage, []string, map[string]Artifact) {
	t.Helper()
	stages, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	for _, stage := range stages {
		inputs := stage.inputs(artifacts, jobs)
		if len(stage.Nodes) > 1 && len(inputs) >= 3 {
			return stage, inputs, artifacts
		}
	}
	t.Fatal("fixture needs a grouped stage with at least three inputs")
	return githubStage{}, nil, nil
}

func carrierTestArchive(t *testing.T, producer, consumer string, artifact Artifact, content string) {
	t.Helper()
	carrierTestWrite(t, filepath.Join(producer, artifact.Path), content)
	archive := filepath.Join(producer, carrierPath(artifact.ID))
	if err := transportarchive.Pack(context.Background(), archive, producer, []string{artifact.Path}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	carrierTestWrite(t, filepath.Join(consumer, carrierPath(artifact.ID)), string(raw))
}

func carrierTestWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func carrierTestResults(t *testing.T, plan *Plan) map[string]any {
	t.Helper()
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]any{}
	for _, stage := range stages {
		results[stage.ID] = map[string]any{"result": "success", "outputs": map[string]string{}}
	}
	for _, node := range plan.Graph.Nodes {
		node, err := plan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		results["build-runner-"+shortStableName(node.RunsOn)] = map[string]any{"result": "success"}
	}
	return results
}

func carrierTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type carrierCancelWriter struct{ cancel context.CancelFunc }

func (writer carrierCancelWriter) Write(raw []byte) (int, error) {
	if bytes.Contains(raw, []byte("Recover: ")) {
		writer.cancel()
	}
	return len(raw), nil
}

type carrierBrokenWriter struct{}

func (carrierBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
