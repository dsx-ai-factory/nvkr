// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func TestDevelopmentCleanupCommandAdmission(t *testing.T) {
	t.Setenv("NVKR_DEV_REGISTRY_CLEANUP", "")
	t.Setenv("GITHUB_TOKEN", "ambient-token-must-not-authorize-cleanup")
	for _, test := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"missing repository", nil, e.CodeUsage},
		{"positional argument", []string{"--repository", "example/project", "unexpected"}, e.CodeUsage},
		{"invalid namespace", []string{"--repository", "example/project/production"}, e.CodeEngineCapability},
		{"dry run requires dedicated credential", []string{"--repository", "example/project"}, e.CodeTrustPolicyMissing},
		{"apply requires dedicated credential", []string{"--repository", "example/project", "--apply"}, e.CodeTrustPolicyMissing},
		{"no alternate token flag", []string{"--repository", "example/project", "--token", "secret"}, e.CodeUsage},
		{"no alternate host flag", []string{"--repository", "example/project", "--api-url", "https://other.invalid"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := invokeLifecycleCLI(t, append([]string{"factory", "github", "cleanup-development"}, test.args...)...)
			assertLifecycleCommandCode(t, err, test.code)
		})
	}
}

// The privileged janitor must execute only trusted default-branch code, even
// when a developer manually dispatches the workflow from a feature branch.
func TestDevelopmentCleanupWorkflowUsesTrustedDefaultBranch(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "nvkr-dev-cleanup.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Concurrency struct {
			Group  string `yaml:"group"`
			Cancel bool   `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			If          string            `yaml:"if"`
			Environment string            `yaml:"environment"`
			Permissions map[string]string `yaml:"permissions"`
			Env         map[string]string `yaml:"env"`
			Steps       []struct {
				ID   string            `yaml:"id"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.On) != 2 || workflow.On["schedule"] == nil || workflow.On["workflow_dispatch"] == nil {
		t.Fatal("cleanup admits triggers beyond schedule/manual dispatch")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) || len(workflow.Env) != 0 {
		t.Fatal("cleanup ambient permissions or credentials changed")
	}
	if workflow.Concurrency.Group != "nvkr-development-registry-cleanup" || workflow.Concurrency.Cancel {
		t.Fatal("cleanup executions must serialize without cancellation")
	}
	job, ok := workflow.Jobs["cleanup"]
	if !ok || len(workflow.Jobs) != 1 || job.Environment != "nvkr-dev-cleanup" || len(job.Env) != 0 || len(job.Permissions) != 0 {
		t.Fatal("cleanup must use its isolated credential environment and inherited read-only token")
	}
	if !strings.Contains(job.If, "github.ref == format('refs/heads/{0}', github.event.repository.default_branch)") || !strings.Contains(job.If, "github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'") {
		t.Fatal("cleanup is not gated to the default branch")
	}
	seenCheckout, seenCleanup := false, false
	for _, step := range job.Steps {
		if step.Uses != "" {
			if !regexp.MustCompile(`^actions/(checkout|setup-go|upload-artifact)@[0-9a-f]{40}$`).MatchString(step.Uses) {
				t.Fatalf("cleanup executes an unadmitted action: %s", step.Uses)
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				seenCheckout = true
				if step.With["persist-credentials"] != "false" || step.With["ref"] != "" || step.With["repository"] != "" {
					t.Fatal("cleanup checkout can select external or feature-branch code")
				}
			}
		}
		for key, value := range step.Env {
			if strings.Contains(value, "secrets.") && (step.ID != "cleanup" || key != "NVKR_DEV_REGISTRY_CLEANUP" || value != "${{ secrets.NVKR_DEV_REGISTRY_CLEANUP }}") {
				t.Fatal("cleanup credential escaped its final command step")
			}
		}
		if step.ID == "cleanup" {
			seenCleanup = true
			if step.Env["NVKR_REPOSITORY"] != "${{ github.repository }}" || step.Env["NVKR_APPLY"] != "${{ github.event_name == 'schedule' || inputs.apply }}" || !strings.Contains(step.Run, `args=(factory github cleanup-development --repository "$NVKR_REPOSITORY")`) {
				t.Fatal("cleanup scope or explicit apply adapter changed")
			}
		}
	}
	if !seenCheckout || !seenCleanup || strings.Count(string(raw), "secrets.NVKR_DEV_REGISTRY_CLEANUP") != 1 {
		t.Fatal("cleanup lost its trusted checkout or narrowly scoped credential")
	}
	dispatch := workflow.On["workflow_dispatch"].(map[string]any)
	inputs := dispatch["inputs"].(map[string]any)
	apply := inputs["apply"].(map[string]any)
	if apply["default"] != false {
		t.Fatal("manual cleanup must default to preview")
	}
}
