// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"sigs.k8s.io/yaml"
)

type nightlyCoordinatorDocument struct {
	Name        string                     `json:"name"`
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress bool   `json:"cancel-in-progress"`
	} `json:"concurrency"`
	Jobs map[string]struct {
		Name           string            `json:"name"`
		RunsOn         string            `json:"runs-on"`
		Needs          []string          `json:"needs"`
		If             string            `json:"if"`
		Environment    string            `json:"environment"`
		TimeoutMinutes int               `json:"timeout-minutes"`
		Permissions    map[string]string `json:"permissions"`
		Env            map[string]string `json:"env"`
		// Authored action inputs use GitHub's Boolean/integer/string scalars;
		// the generated workflow's string-only With model is intentionally narrower.
		Steps []struct {
			ID   string            `json:"id"`
			Name string            `json:"name"`
			Run  string            `json:"run"`
			Uses string            `json:"uses"`
			If   string            `json:"if"`
			Env  map[string]string `json:"env"`
			With map[string]any    `json:"with"`
		} `json:"steps"`
		Outputs     map[string]string `json:"outputs"`
		Strategy    json.RawMessage   `json:"strategy"`
		Concurrency struct {
			Group            string `json:"group"`
			CancelInProgress bool   `json:"cancel-in-progress"`
		} `json:"concurrency"`
	} `json:"jobs"`
}

func nightlyCoordinatorFixture(t *testing.T) (nightlyCoordinatorDocument, []map[string]string) {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow nightlyCoordinatorDocument
	if err := yaml.UnmarshalStrict(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../definitions/nightly-profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var profiles []map[string]string
	if err := json.Unmarshal(raw, &profiles); err != nil {
		t.Fatal(err)
	}
	return workflow, profiles
}

func TestNightlyCoordinatorMaintainsExplicitProducerAndRuntimeBoundary(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	if len(workflow.On) != 2 || workflow.On["schedule"] == nil || workflow.On["workflow_dispatch"] == nil ||
		!reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) ||
		workflow.Concurrency.CancelInProgress || workflow.Concurrency.Group != "nvkr-nightly-main" {
		t.Fatal("nightly triggers, concurrency or root permissions changed")
	}
	build, runtime := workflow.Jobs["build"], workflow.Jobs["runtime-test"]
	selection := workflow.Jobs["select-runtime"]
	if selection.RunsOn != "ubuntu-24.04" ||
		!reflect.DeepEqual(selection.Permissions, map[string]string{"contents": "read", "actions": "read"}) ||
		!reflect.DeepEqual(runtime.Needs, []string{"select-runtime"}) ||
		!strings.Contains(runtime.If, "needs.select-runtime.outputs.available == 'true'") {
		t.Fatal("runtime target allocated before completed candidate selection")
	}
	if !reflect.DeepEqual(build.Permissions, map[string]string{"contents": "read", "actions": "write"}) ||
		!reflect.DeepEqual(runtime.Permissions, map[string]string{"contents": "read", "actions": "read", "packages": "read"}) ||
		runtime.RunsOn != "arc-arm64-kvm" || runtime.Environment != "nkd-arc" ||
		runtime.Concurrency.CancelInProgress || runtime.Concurrency.Group != "nvkr-nightly-native-arm64-runtime" ||
		runtime.TimeoutMinutes != 360 {
		t.Fatal("dispatch credentials escaped into native runtime or shared target admission changed")
	}
	executed, checkedSource, publicUpload := false, false, false
	for _, step := range runtime.Steps {
		if step.With["repository"] == "dsx-ai-factory/nvke" {
			checkedSource = step.With["ref"] == "${{ matrix.harness_commit }}" &&
				step.With["persist-credentials"] == false && step.With["path"] == ".nightly-private/nvke"
		}
		if strings.Contains(step.Run, "go test ./internal/nvkedemo -run '^TestNightlyPublishedCandidate$'") {
			executed = step.Env["NVKR_NIGHTLY_ENABLE"] == "1" &&
				step.Env["NVKR_NIGHTLY_NETWORK_HELPER"] == "${{ github.workspace }}/bin/nvke-demo" &&
				strings.Contains(step.Run, "-list '^TestNightlyPublishedCandidate$'") &&
				strings.Contains(step.Run, "-timeout 330m -count=1 -v") &&
				strings.Contains(step.Run, `.phase == "runtime" and .runtimePassed == true`) &&
				strings.Contains(step.Run, `.coordinatorRun == $run and .coordinatorAttempt == $attempt`) &&
				strings.Contains(step.Run, `.sourceCommit == $sha`)
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			publicUpload = step.If == "always()" &&
				step.With["path"] == "${{ runner.temp }}/nightly-reports-${{ matrix.name }}/*"
		}
	}
	if !executed || !checkedSource || !publicUpload || len(profiles) != 2 {
		t.Fatal("nightly omitted real suite execution, exact source or private/public report separation")
	}
	for _, profile := range profiles {
		input, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", profile["definition"]))
		if err != nil {
			t.Fatal(err)
		}
		environment, err := definition.LoadEnvironment(t.Context(), filepath.Join("../..", profile["environment"]))
		if err != nil {
			t.Fatal(err)
		}
		if environment.GitHubBundle != profile["bundle"] ||
			profile["name"] != profile["bundle"] || profile["build_workflow"] != "nvkr-run-"+profile["bundle"]+".yml" ||
			profile["plan"] != ".github/nvkr-run-"+profile["bundle"]+"/plan.json" {
			t.Fatal("nightly reduced output membership or changed the exact generated producer")
		}
		plan, err := factory.LoadPlanContext(t.Context(), filepath.Join("../..", profile["plan"]))
		if err != nil {
			t.Fatal(err)
		}
		membership := make(map[string]bool, len(plan.LifecycleOutputs))
		for _, output := range plan.LifecycleOutputs {
			membership[output.Name] = true
		}
		if len(input.Outputs) != len(membership) {
			t.Fatal("generated Plan changed Definition output membership")
		}
		disk, images := false, false
		for _, output := range input.Outputs {
			if !membership[output.Name] {
				t.Fatalf("output %q absent from generated Plan", output.Name)
			}
			disk = disk || output.Name == profile["disk_output"]
			images = images || output.Name == profile["profiles_output"]
		}
		if !disk || !images {
			t.Fatal("profile selects absent disk or controller profile output")
		}
	}
}

func TestNightlyCoordinatorSelectionFailsClosedBeforeDispatch(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("nightly contract tests require %s: %v", tool, err)
		}
	}
	var script string
	for _, step := range workflow.Jobs["select"].Steps {
		if step.ID == "profiles" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("nightly select job omitted maintained profile admission")
	}
	for _, test := range []struct {
		name      string
		selection string
		mutation  string
		want      int
	}{
		{"maintained all", "all", "", 2},
		{"maintained one", "nvke-nightly-arm64", "", 1},
		{"unknown", "unknown", "", 0},
		{"unreviewed harness", "all", "harness_commit", 0},
		{"unsupported runtime", "all", "runtime_target", 0},
		{"unsupported network", "all", "network", 0},
		{"duplicate profile", "all", "duplicate", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := make([]map[string]string, len(profiles))
			for i, profile := range profiles {
				fixture[i] = make(map[string]string, len(profile))
				for key, value := range profile {
					fixture[i][key] = value
				}
			}
			if test.mutation == "duplicate" {
				fixture = append(fixture, fixture[0])
			} else if test.mutation != "" {
				fixture[0][test.mutation] = "unsupported"
			}
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "definitions"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "definitions/nightly-profiles.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Dir = root
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "PROFILE=" + test.selection,
				"GITHUB_OUTPUT=" + output, "RUNNER_TEMP=" + root}
			log, err := command.CombinedOutput()
			if test.want == 0 {
				if err == nil {
					t.Fatal("invalid profile emitted a runtime matrix")
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("rejected selection emitted outputs before dispatch")
				}
				return
			}
			if err != nil {
				t.Fatalf("read-only fixture selection failed: %v %s", err, log)
			}
			raw, err = os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var matrix struct {
				Include []map[string]string `json:"include"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(string(raw)), "matrix=")), &matrix); err != nil || len(matrix.Include) != test.want {
				t.Fatalf("selected wrong profile closure: %v %s", err, raw)
			}
		})
	}
}

func TestNightlyRuntimeSelectionDoesNotAllocateUnavailableCandidates(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	var script string
	for _, step := range workflow.Jobs["select-runtime"].Steps {
		if step.ID == "completed" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("completed candidate selector missing")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("nightly contract tests require %s: %v", tool, err)
		}
	}
	for _, test := range []struct {
		name      string
		available int
		mutation  string
		reject    bool
	}{
		{"no locator", 0, "", false},
		{"one completed project", 1, "", false},
		{"both completed projects", 2, "", false},
		{"wrong source", 1, "sha", true},
		{"expired locator", 1, "expired", true},
		{"duplicate locator", 1, "duplicate", true},
		{"unknown matrix", 1, "matrix", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			fake := `#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if [[ "$argument" == repos/* ]]; then
    case "$argument" in
      *name=nightly-build-nvke-nightly-arm64-101-3\&per_page=100) cat "$FAKE_GH_ROOT/0.json"; exit ;;
      *name=nightly-build-nvke-nightly-upstream-ovn-arm64-101-3\&per_page=100) cat "$FAKE_GH_ROOT/1.json"; exit ;;
      *) exit 99 ;;
    esac
  fi
done
exit 99
`
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			matrix := make([]map[string]string, len(profiles))
			for i, p := range profiles {
				matrix[i] = map[string]string{"name": p["name"], "harness_commit": p["harness_commit"]}
				artifacts := []map[string]any{}
				if i < test.available {
					artifact := map[string]any{"name": "nightly-build-" + p["name"] + "-101-3", "expired": false,
						"workflow_run": map[string]string{"head_sha": nightlySHA}}
					if test.mutation == "sha" {
						artifact["workflow_run"] = map[string]string{"head_sha": strings.Repeat("b", 40)}
					}
					if test.mutation == "expired" {
						artifact["expired"] = true
					}
					artifacts = append(artifacts, artifact)
					if test.mutation == "duplicate" {
						artifacts = append(artifacts, artifact)
					}
				}
				fixtureJSON(t, filepath.Join(root, string(rune('0'+i))+".json"), map[string]any{"total_count": len(artifacts), "artifacts": artifacts})
			}
			if test.mutation == "matrix" {
				matrix[0]["name"] = "foreign"
			}
			raw, err := json.Marshal(map[string]any{"include": matrix})
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "outputs")
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "FAKE_GH_ROOT=" + root,
				"GITHUB_OUTPUT=" + out, "GITHUB_REPOSITORY=example/nvkr", "GITHUB_RUN_ID=101", "GITHUB_RUN_ATTEMPT=3",
				"GITHUB_SHA=" + nightlySHA, "MATRIX=" + string(raw)}
			log, err := command.CombinedOutput()
			if test.reject {
				if err == nil {
					t.Fatalf("invalid locator admitted: %s", log)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("rejected locator emitted runtime matrix")
				}
				return
			}
			if err != nil {
				t.Fatalf("selector failed: %v %s", err, log)
			}
			raw, err = os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			var selected struct {
				Include []map[string]string `json:"include"`
			}
			if len(lines) != 2 {
				t.Fatalf("invalid selector output: %s", raw)
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], "matrix=")), &selected); err != nil {
				t.Fatal(err)
			}
			if len(selected.Include) != test.available || lines[1] != "available="+map[bool]string{true: "true", false: "false"}[test.available > 0] {
				t.Fatalf("wrong runtime eligibility: %s", raw)
			}
		})
	}
}
