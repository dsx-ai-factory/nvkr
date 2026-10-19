// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

type mergeGateStep struct {
	storageWorkflowStep
	ID    string `json:"id"`
	Shell string `json:"shell"`
}

type mergeGateJob struct {
	Needs    []string          `json:"needs"`
	Outputs  map[string]string `json:"outputs"`
	Steps    []mergeGateStep   `json:"steps"`
	Strategy struct {
		FailFast *bool  `json:"fail-fast"`
		Matrix   string `json:"matrix"`
	} `json:"strategy"`
}

func readMergeGateJobs(t *testing.T) map[string]mergeGateJob {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/merge-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]mergeGateJob `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

func TestMergeGateRequiresEveryCodeJob(t *testing.T) {
	gate := readMergeGateJobs(t)["gate"]
	codeJobs := []string{"check", "lint", "vulnerabilities", "tests"}
	for _, name := range append(slices.Clone(codeJobs), "check-skip", "check-paths") {
		if !slices.Contains(gate.Needs, name) {
			t.Fatalf("gate does not depend on %s", name)
		}
	}
	if len(gate.Steps) != 1 || gate.Steps[0].Run == "" {
		t.Fatal("missing executable aggregate gate")
	}
	cases := []struct {
		name, code, job, result string
		wantSuccess             bool
	}{
		{"all code checks pass", "true", "", "", true},
		{"unchanged code", "false", "", "", true},
		{"missing skip companion", "false", "check-skip", "skipped", false},
		{"classifier failed", "true", "check-paths", "failure", false},
		{"another gate failed", "true", "docs", "failure", false},
		{"unknown classification", "", "", "", false},
	}
	for _, job := range codeJobs {
		for _, result := range []string{"skipped", "failure", "cancelled"} { //nolint:misspell // GitHub result enum spelling.
			cases = append(cases, struct {
				name, code, job, result string
				wantSuccess             bool
			}{job + " " + result, "true", job, result, false})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			needs := map[string]map[string]string{}
			for _, job := range gate.Needs {
				needs[job] = map[string]string{"result": "success"}
			}
			if tc.code == "true" {
				needs["check-skip"]["result"] = "skipped"
			} else {
				for _, job := range codeJobs {
					needs[job]["result"] = "skipped"
				}
			}
			if tc.job != "" {
				needs[tc.job]["result"] = tc.result
			}
			results := make([]string, 0, len(gate.Needs))
			for _, job := range gate.Needs {
				results = append(results, needs[job]["result"])
			}
			encodedNeeds, err := json.Marshal(needs)
			if err != nil {
				t.Fatal(err)
			}
			encodedResults, err := json.Marshal(results)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer(
				"${{ toJSON(needs) }}", string(encodedNeeds),
				"${{ toJSON(needs.*.result) }}", string(encodedResults),
				"${{ needs.check-paths.result }}", needs["check-paths"]["result"],
				"${{ needs.check-paths.outputs.code }}", tc.code,
			).Replace(gate.Steps[0].Run)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "bash", "-eu", "-c", script).CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("gate success=%t, want %t: %v\n%s", err == nil, tc.wantSuccess, err, output)
			}
		})
	}
}

func TestMergeGateTestMatrix(t *testing.T) {
	jobs := readMergeGateJobs(t)
	producer, tests := jobs["check-paths"], jobs["tests"]
	if producer.Outputs["test-matrix"] != "${{ steps.test-matrix.outputs.matrix }}" {
		t.Fatal("check-paths must expose the matrix producer's output")
	}
	if !slices.Contains(tests.Needs, "check-paths") ||
		tests.Strategy.Matrix != "${{ fromJSON(needs.check-paths.outputs.test-matrix) }}" {
		t.Fatal("test strategy must consume the complete producer matrix")
	}
	if tests.Strategy.FailFast == nil || *tests.Strategy.FailFast {
		t.Fatal("test matrix must explicitly disable fail-fast to retain every shard's diagnostics")
	}
	step := mergeGateStepByID(t, producer, "test-matrix")
	const matrix = `{"include":[{"group":"factory","index":2}]}`
	for _, tc := range []struct {
		name string
		exit string
		code int
	}{
		{"success", "0", 0},
		{"producer failure", "23", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fake := `#!/bin/sh
[ "$*" = '-B scripts/ci-test-shard.py --matrix' ] || exit 99
printf '%s\n' "$FAKE_MATRIX"
exit "$FAKE_EXIT"
`
			if err := os.WriteFile(filepath.Join(root, "python3"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(root, "outputs")
			output, code := runMergeGateStep(t, step, root, []string{
				"GITHUB_OUTPUT=" + outputPath, "FAKE_MATRIX=" + matrix, "FAKE_EXIT=" + tc.exit,
			})
			if code != tc.code {
				t.Fatalf("matrix step exited %d, want %d: %s", code, tc.code, output)
			}
			raw, err := os.ReadFile(outputPath)
			if tc.code != 0 {
				if !os.IsNotExist(err) {
					t.Fatalf("failed producer published a matrix: %s (read error: %v)", raw, err)
				}
				return
			}
			if err != nil || string(raw) != "matrix="+matrix+"\n" {
				t.Fatalf("matrix output changed: %s (read error: %v)", raw, err)
			}
		})
	}
}

func TestMergeGateShardPipeline(t *testing.T) {
	step := mergeGateStepByID(t, readMergeGateJobs(t)["tests"], "tests")
	if step.Shell != "bash" {
		t.Fatal("shard pipeline requires explicit bash to enable GitHub's pipefail behavior")
	}
	for name, want := range map[string]string{
		"TEST_SHARD_GROUP": "${{ matrix.group }}",
		"TEST_SHARD_INDEX": "${{ matrix.index }}",
	} {
		if step.Env[name] != want {
			t.Fatalf("%s must bind to %s, got %q", name, want, step.Env[name])
		}
	}
	const diagnostic = `{"Action":"output","Package":"fixture","Output":"partial diagnostic\n"}` + "\n"
	for _, tc := range []struct {
		name string
		exit string
		code int
	}{
		{"success", "0", 0},
		{"shard failure", "23", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fake := `#!/bin/sh
[ "$#" -eq 1 ] && [ "$1" = test-shard ] || exit 99
[ "$TEST_SHARD_GROUP" = factory ] && [ "$TEST_SHARD_INDEX" = 2 ] || exit 99
printf '%s' "$FAKE_DIAGNOSTIC"
exit "$FAKE_EXIT"
`
			if err := os.WriteFile(filepath.Join(root, "make"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			env := []string{"FAKE_DIAGNOSTIC=" + diagnostic, "FAKE_EXIT=" + tc.exit}
			for name, value := range step.Env {
				value = strings.NewReplacer("${{ matrix.group }}", "factory", "${{ matrix.index }}", "2").Replace(value)
				env = append(env, name+"="+value)
			}
			output, code := runMergeGateStep(t, step, root, env)
			if code != tc.code {
				t.Fatalf("shard pipeline exited %d, want %d: %s", code, tc.code, output)
			}
			raw, err := os.ReadFile(filepath.Join(root, "go-test.jsonl"))
			if err != nil || string(raw) != diagnostic || output != diagnostic {
				t.Fatalf("shard diagnostics lost: file=%q, output=%q, read error=%v", raw, output, err)
			}
		})
	}
}

func mergeGateStepByID(t *testing.T, job mergeGateJob, id string) mergeGateStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.ID == id {
			if step.Run == "" || step.If != "" || step.ContinueOnError != nil && step.ContinueOnError != false {
				t.Fatalf("step %s must run unconditionally and propagate failures", id)
			}
			return step
		}
	}
	t.Fatalf("missing step %s", id)
	return mergeGateStep{}
}

func runMergeGateStep(t *testing.T, step mergeGateStep, root string, env []string) (string, int) {
	t.Helper()
	script := filepath.Join(root, "step.sh")
	if err := os.WriteFile(script, []byte(step.Run), 0o600); err != nil {
		t.Fatal(err)
	}
	// GitHub's default Linux shell uses -e; explicit bash additionally enables
	// pipefail. Execute the declared script with the matching runner semantics.
	args := []string{"-e", script}
	switch step.Shell {
	case "":
	case "bash":
		args = []string{"--noprofile", "--norc", "-e", "-o", "pipefail", script}
	default:
		t.Fatalf("unsupported workflow shell %q", step.Shell)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", args...)
	command.Dir = root
	command.Env = append([]string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"), "RUNNER_TEMP=" + root}, env...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("workflow step exceeded deadline: %v", ctx.Err())
	}
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatal(err)
		}
		return string(output), exitError.ExitCode()
	}
	return string(output), 0
}
