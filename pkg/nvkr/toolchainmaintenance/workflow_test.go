// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestWorkflowKeepsPreparationPublicationAndReviewSeparate(t *testing.T) {
	raw, err := os.ReadFile("../../../" + Workflow)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]any    `json:"on"`
		Permissions map[string]string `json:"permissions"`
		Concurrency struct {
			Cancel bool `json:"cancel-in-progress"`
		} `json:"concurrency"`
		Jobs map[string]struct {
			If          string            `json:"if"`
			Needs       any               `json:"needs"`
			Environment string            `json:"environment"`
			Timeout     int               `json:"timeout-minutes"`
			Permissions map[string]string `json:"permissions"`
			Steps       []struct {
				Name string            `json:"name"`
				Uses string            `json:"uses"`
				Run  string            `json:"run"`
				Env  map[string]string `json:"env"`
				With map[string]any    `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Permissions["contents"] != "read" || workflow.Permissions["actions"] != "read" || workflow.Concurrency.Cancel {
		t.Fatal("unsafe workflow token or publication cancellation")
	}
	for _, trigger := range []string{"pull_request", "pull_request_target", "workflow_run"} {
		if _, present := workflow.On[trigger]; present {
			t.Fatalf("untrusted publication trigger %s", trigger)
		}
	}
	if !strings.Contains(string(raw), "cron: '0 6 * * 1'") {
		t.Fatal("owner-approved weekly schedule changed")
	}
	for _, key := range []string{"measure", "admit"} {
		if !strings.Contains(workflow.Jobs[key].If, "github.repository == 'dsx-ai-factory/nvkr'") || !strings.Contains(workflow.Jobs[key].If, "github.ref == 'refs/heads/main'") {
			t.Fatalf("%s lacks repository/main admission", key)
		}
	}
	if workflow.Jobs["publish"].Environment != "nvkr-release" || workflow.Jobs["publish"].Needs != "admit" {
		t.Fatal("publication bypasses exception/environment gates")
	}
	proposalNeeds, ok := workflow.Jobs["propose"].Needs.([]any)
	if !ok || len(proposalNeeds) != 3 || proposalNeeds[2] != "verify-candidate" {
		t.Fatal("review can precede native read-back")
	}
	for jobName, job := range workflow.Jobs {
		if job.Timeout <= 0 {
			t.Fatalf("unbounded job %s", jobName)
		}
		for _, step := range job.Steps {
			if step.Uses != "" && !strings.HasPrefix(step.Uses, "./") {
				parts := strings.Split(step.Uses, "@")
				if len(parts) != 2 || !commitPattern.MatchString(parts[1]) {
					t.Fatalf("mutable action %s", step.Uses)
				}
			}
			for _, value := range step.Env {
				if strings.Contains(value, "secrets.NVKR_REGISTRY_WRITE") && jobName != "publish" {
					t.Fatalf("write credential escaped publication: %s", jobName)
				}
			}
			if jobName == "publish" && strings.Contains(step.Run, "docker run") {
				t.Fatal("producer execution in credential-bearing publication")
			}
			if jobName == "propose" && strings.Contains(step.Run, "--operation repin") && (!strings.Contains(step.Run, "make workflows-generate workflows-check") || !strings.Contains(step.Run, "git diff --check")) {
				t.Fatal("pin proposal does not regenerate/check through owners")
			}
		}
	}
	if !strings.Contains(string(raw), "--assignee njhensley") {
		t.Fatal("refresh failures lack the named owner")
	}
}
