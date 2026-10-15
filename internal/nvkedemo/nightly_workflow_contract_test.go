// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"go.yaml.in/yaml/v3"
)

type nightlyWorkflowStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	With map[string]string `yaml:"with"`
}

func nightlyWorkflowSteps(t *testing.T) []nightlyWorkflowStep {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []nightlyWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["runtime-test"].Steps
	if len(steps) == 0 {
		t.Fatal("missing nightly runtime steps")
	}
	return steps
}

func nightlyWorkflowScript(t *testing.T, id string) string {
	t.Helper()
	script := ""
	for _, step := range nightlyWorkflowSteps(t) {
		if step.ID == id {
			if script != "" {
				t.Fatalf("duplicate authored step %s", id)
			}
			script = step.Run
		}
	}
	if script == "" {
		t.Fatalf("missing authored step %s", id)
	}
	return script
}

func nightlyWorkflowWrite(t *testing.T, root, name string, value any) {
	t.Helper()
	// These are provider/authored descriptions, including intentionally invalid
	// fractional IDs; no logical identity is derived from this fixture encoding.
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nightlyWorkflowRun(t *testing.T, root, script string, values map[string]string) (string, error) {
	t.Helper()
	output := filepath.Join(root, "github-output")
	values["PATH"], values["GITHUB_OUTPUT"] = os.Getenv("PATH"), output
	env := SetEnv(nil, values)
	bash, err := commandPath("bash", env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandPath("jq", env); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bash, "-c", script)
	command.Env, command.Dir = env, root
	if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
		t.Fatal(err)
	}
	diagnostic, runErr := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("authored script exceeded owned deadline: %v", ctx.Err())
	}
	raw, readErr := os.ReadFile(output)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if runErr != nil {
		t.Logf("authored script rejection: %s", diagnostic)
	}
	return string(raw), runErr
}

func TestNightlyWorkflowSuiteSelectionMatchesGoCohortOwner(t *testing.T) {
	script := nightlyWorkflowScript(t, "suite")
	profile, build := nightlyIdentityFixture()
	for _, test := range []struct {
		name   string
		change func(*factory.Plan)
		legacy bool
	}{
		{"current frozen source", func(*factory.Plan) {}, false},
		{"legacy configured source", func(*factory.Plan) {}, true},
		{"missing role", func(p *factory.Plan) { p.Builds = p.Builds[1:] }, false},
		{"mixed revisions", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Commit = strings.Repeat("e", 40) }, false},
		{"duplicate replaces missing role", func(p *factory.Plan) { p.Builds[len(p.Builds)-1] = p.Builds[0] }, false},
		{"extra role producer", func(p *factory.Plan) {
			p.Builds[0].ResolvedComponents = append(p.Builds[0].ResolvedComponents, p.Builds[1].ResolvedComponents[0])
		}, false},
		{"wrong repository", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Repo = "https://github.com/foreign/nvke" }, false},
		{"repository suffix attack", func(p *factory.Plan) {
			p.Builds[0].ResolvedComponents[0].Repo = "https://github.com/dsx-ai-factory/nvke.attacker"
		}, false},
		{"acquired producer", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].FromSource = false }, false},
		{"invalid revision", func(p *factory.Plan) { p.Builds[0].ResolvedComponents[0].Commit = "main" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _ := nightlyRuntimeCompiledFixture(t, profile, build, strings.Repeat("d", 40))
			test.change(plan)
			root := t.TempDir()
			nightlyWorkflowWrite(t, root, "inputs/testing/nightly-profiles.json", []NightlyProfile{profile})
			values := map[string]string{"PROFILE": profile.Name, "PINNED_SUITE": profile.HarnessCommit, "RUNTIME_PLAN": "true"}
			expected, expectedErr := nightlyPlanHarnessCommit(plan, profile)
			if test.legacy {
				values["RUNTIME_PLAN"] = "false"
				expected, expectedErr = profile.HarnessCommit, nil
				// The legacy script must not require a runtime artifact at all.
			} else {
				nightlyWorkflowWrite(t, root, ".nightly-private/runtime-inputs/plan.json", plan)
			}
			output, err := nightlyWorkflowRun(t, root, script, values)
			if expectedErr != nil {
				if err == nil || output != "" {
					t.Fatalf("authored suite selector accepted rejected Go cohort: output=%q error=%v owner=%v", output, err, expectedErr)
				}
			} else if err != nil || output != "commit="+expected+"\n" {
				t.Fatalf("suite selection differs from Go owner: got=%q error=%v expected=%q", output, err, expected)
			}
		})
	}
}

func TestNightlyWorkflowCandidateSelectsOnlyExactCycleAndBooleanPolicy(t *testing.T) {
	script := nightlyWorkflowScript(t, "candidate")
	profile, build := nightlyIdentityFixture()
	for _, test := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		pass   bool
		policy string
	}{
		{"runtime enabled", func(map[string]any, map[string]any) {}, true, "true"},
		{"legacy absent policy", func(_ map[string]any, env map[string]any) { delete(env, "githubRuntimePlan") }, true, "false"},
		{"legacy explicit false", func(_ map[string]any, env map[string]any) { env["githubRuntimePlan"] = false }, true, "false"},
		{"string policy", func(_ map[string]any, env map[string]any) { env["githubRuntimePlan"] = "true" }, false, ""},
		{"wrong profile", func(b map[string]any, _ map[string]any) { b["profile"].(map[string]any)["name"] = "other" }, false, ""},
		{"wrong source", func(b map[string]any, _ map[string]any) { b["sourceCommit"] = strings.Repeat("e", 40) }, false, ""},
		{"wrong coordinator", func(b map[string]any, _ map[string]any) { b["coordinatorRun"] = "101" }, false, ""},
		{"wrong attempt", func(b map[string]any, _ map[string]any) { b["coordinatorAttempt"] = "3" }, false, ""},
		{"numeric coordinator", func(b map[string]any, _ map[string]any) { b["coordinatorRun"] = 100 }, false, ""},
		{"producer retry", func(b map[string]any, _ map[string]any) { b["producerAttempt"] = 2 }, false, ""},
		{"string producer", func(b map[string]any, _ map[string]any) { b["producerRun"] = "200" }, false, ""},
		{"fractional producer", func(b map[string]any, _ map[string]any) { b["producerRun"] = 200.5 }, false, ""},
		{"zero producer", func(b map[string]any, _ map[string]any) { b["producerRun"] = 0 }, false, ""},
		{"imprecise producer", func(b map[string]any, _ map[string]any) { b["producerRun"] = json.Number("9007199254740992") }, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			raw, err := json.Marshal(build)
			if err != nil {
				t.Fatal(err)
			}
			var locator map[string]any
			if err := json.Unmarshal(raw, &locator); err != nil {
				t.Fatal(err)
			}
			environment := map[string]any{"githubRuntimePlan": true}
			test.change(locator, environment)
			nightlyWorkflowWrite(t, root, "inputs/testing/nightly-profiles.json", []NightlyProfile{profile})
			nightlyWorkflowWrite(t, root, profile.Environment, environment)
			nightlyWorkflowWrite(t, root, ".nightly-private/build/build.json", locator)
			values := map[string]string{"PROFILE": profile.Name, "GITHUB_SHA": build.SourceCommit, "GITHUB_RUN_ID": build.CoordinatorRun, "GITHUB_RUN_ATTEMPT": build.CoordinatorAttempt}
			output, err := nightlyWorkflowRun(t, root, script, values)
			if test.pass {
				if err != nil || output != "producer_run=200\nruntime_plan="+test.policy+"\n" {
					t.Fatalf("candidate selection: output=%q error=%v", output, err)
				}
			} else if err == nil || output != "" {
				t.Fatalf("invalid candidate emitted outputs: %q, %v", output, err)
			}
		})
	}
}

func TestNightlyWorkflowFreshArtifactAndSuiteCheckoutAreOrderedAndBound(t *testing.T) {
	steps := nightlyWorkflowSteps(t)
	candidate, download, suite, checkout := -1, -1, -1, -1
	for index, step := range steps {
		switch {
		case step.ID == "candidate":
			candidate = index
		case step.With["name"] == "nvkr-runtime-inputs":
			download = index
			if step.If != "steps.candidate.outputs.runtime_plan == 'true'" || step.With["run-id"] != "${{ steps.candidate.outputs.producer_run }}" || step.With["github-token"] != "${{ github.token }}" || step.With["path"] != ".nightly-private/runtime-inputs" {
				t.Fatal("runtime artifact download lost exact producer or policy binding")
			}
		case step.ID == "suite":
			suite = index
		case step.With["repository"] == "dsx-ai-factory/nvke":
			checkout = index
			if step.With["ref"] != "${{ steps.suite.outputs.commit }}" || step.With["persist-credentials"] != "false" {
				t.Fatal("NVKE checkout is not bound to derived exact suite revision")
			}
		}
	}
	if candidate < 0 || !(candidate < download && download < suite && suite < checkout) {
		t.Fatalf("producer/Plan/suite ordering changed: %d/%d/%d/%d", candidate, download, suite, checkout)
	}
}
