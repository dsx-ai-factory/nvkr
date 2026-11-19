// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestRuntimeDiskStageRequiresExactBootstrap(t *testing.T) {
	plan := runtimeConsumerPlanFixture(t)
	plan.Actions = arcTestPlan(t, "linux/amd64").Actions
	stage := githubStage{DiskOutput: "node-image", diskExecutionBudget: 485 * time.Second,
		Nodes: []Node{{ID: "node-image/build-image", Platform: "linux/amd64"}}}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	var fragment bytes.Buffer
	writeGitHubPackerDiagnosticStep(&fragment, plan, stage, actions)
	var diagnostic struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict([]byte("steps:\n"+fragment.String()), &diagnostic); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*githubStep)
	}{
		{"canonical bootstrap", nil},
		{"legacy direct executor", func(step *githubStep) { step.Run = githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput) }},
		{"missing preflight", func(step *githubStep) { _, step.Run, _ = strings.Cut(step.Run, " && ") }},
		{"ambient environment", func(step *githubStep) { step.Run = strings.ReplaceAll(step.Run, "/usr/bin/env -i", "/usr/bin/env") }},
		{"changed watchdog", func(step *githubStep) { step.Run = strings.ReplaceAll(step.Run, "515s", "516s") }},
		{"other output", func(step *githubStep) { step.Run = strings.ReplaceAll(step.Run, "node-image", "other-image") }},
		{"conditional execution", func(step *githubStep) { step.If = "false" }},
		{"added credential", func(step *githubStep) { step.Env = map[string]string{sourceTokenEnv: "synthetic-token"} }},
		{"changed step timeout", func(step *githubStep) { step.TimeoutMinutes++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			step := githubStep{ID: "run-disk", Run: githubDiskCommand(plan, stage), TimeoutMinutes: githubDiskStepTimeoutMinutes(stage)}
			if test.change != nil {
				test.change(&step)
			}
			err := validateGitHubStageExecution(plan, stage, []githubStep{step, diagnostic.Steps[0]})
			if test.change != nil {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeDiskBootstrapScrubsCredentialsAndRequiresPreflight(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash required for generated bootstrap contract")
	}
	plan := runtimeConsumerPlanFixture(t)
	stage := githubStage{DiskOutput: "node-image", diskExecutionBudget: 485 * time.Second}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "preflight rejects"}[fail], func(t *testing.T) {
			directory := t.TempDir()
			helper := `#!/bin/sh
set -eu
case "$3" in
admit-disk) [ "${ADMIT_FAIL-}" != 1 ] ;;
run-disk) env > child-environment; printf '%s\n' "$@" > child-arguments ;;
*) exit 91 ;;
esac
`
			if err := os.WriteFile(filepath.Join(directory, "nvkr-exec"), []byte(helper), 0o700); err != nil {
				t.Fatal(err)
			}
			timeout := filepath.Join(directory, "timeout-fixture")
			if err := os.WriteFile(timeout, []byte("#!/bin/sh\nset -eu\n[ \"$1\" = --signal=TERM ] && [ \"$2\" = --kill-after=30s ] && [ \"$3\" = 515s ]\nshift 3\nexec \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(githubDiskCommand(plan, stage), "/usr/bin/sudo", "fixture_sudo")
			script = strings.ReplaceAll(script, "/usr/bin/timeout", shellQuote(timeout))
			script = `fixture_sudo() {
 [ "$1" = -n ] && [ "$2" = -- ] || return 92
 shift 2
 printf called > sudo-called
 "$@"
}
` + script
			command := exec.CommandContext(t.Context(), bash, "-c", script)
			command.Dir = directory
			command.Env = append(os.Environ(), "GITHUB_TOKEN=synthetic-token", "ACTIONS_RUNTIME_TOKEN=synthetic-runtime-token",
				"AWS_SECRET_ACCESS_KEY=synthetic-secret", "HOME=/private/ambient-home", "DOCKER_CONFIG=/private/ambient-auth",
				"GITHUB_RUN_ID=123", "GITHUB_OUTPUT=/descriptive/output", "ADMIT_FAIL="+map[bool]string{true: "1", false: "0"}[fail])
			output, runErr := command.CombinedOutput()
			if fail {
				if runErr == nil {
					t.Fatal("rejected preflight launched privileged owner")
				}
				if _, err := os.Stat(filepath.Join(directory, "sudo-called")); !os.IsNotExist(err) {
					t.Fatal("sudo reached after failed preflight")
				}
				return
			}
			if runErr != nil {
				t.Fatalf("bootstrap failed: %v %s", runErr, output)
			}
			raw, err := os.ReadFile(filepath.Join(directory, "child-environment"))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"synthetic-", "/private/ambient", "ADMIT_FAIL", "SUDO_UID"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("privileged environment retained %q", forbidden)
				}
			}
			for _, required := range []string{"HOME=/dev/null\n", "DOCKER_CONFIG=/proc/self/fd\n", "GITHUB_RUN_ID=123\n", "GITHUB_OUTPUT=/descriptive/output\n"} {
				if !strings.Contains(string(raw), required) {
					t.Fatalf("privileged environment omitted %q", required)
				}
			}
			args, err := os.ReadFile(filepath.Join(directory, "child-arguments"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "factory\ngithub\nrun-disk\n--plan\n") || !strings.HasSuffix(string(args), "--output\nnode-image\n") {
				t.Fatal("bootstrap changed its exact selected operation")
			}
		})
	}
	plan.Environment.Runtime.ProducedRuntimes = nil
	if got := githubDiskCommand(plan, stage); got != githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput) {
		t.Fatal("ordinary disk worker acquired privileged bootstrap")
	}
}
