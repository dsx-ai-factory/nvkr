// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Keep the nightly's public-runner and non-distribution boundaries explicit.
func TestNightlyUsesOnlyHostedBuildAndGoE2E(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]json.RawMessage `json:"on"`
		Permissions map[string]string          `json:"permissions"`
		Concurrency struct {
			Group            string `json:"group"`
			CancelInProgress *bool  `json:"cancel-in-progress"`
		} `json:"concurrency"`
		Jobs map[string]struct {
			If             string            `json:"if"`
			RunsOn         string            `json:"runs-on"`
			Environment    string            `json:"environment"`
			TimeoutMinutes int               `json:"timeout-minutes"`
			Permissions    map[string]string `json:"permissions"`
			Steps          []struct {
				Run  string            `json:"run"`
				Uses string            `json:"uses"`
				Env  map[string]string `json:"env"`
				With map[string]any    `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	var schedule []struct{ Cron string }
	if err := json.Unmarshal(workflow.On["schedule"], &schedule); err != nil || len(schedule) != 1 || schedule[0].Cron != "17 3 * * *" {
		t.Fatalf("nightly must retain its daily trigger: %v", err)
	}
	if len(workflow.On) != 2 || workflow.On["workflow_dispatch"] == nil || len(workflow.Jobs) != 1 || !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("nightly must retain ordinary manual dispatch and read-only permissions")
	}
	if workflow.Concurrency.Group != "nvkr-nightly-${{ github.ref }}" || workflow.Concurrency.CancelInProgress == nil || *workflow.Concurrency.CancelInProgress {
		t.Fatal("nightly must serialize each branch without canceling its owned cleanup")
	}
	if strings.Count(string(raw), "MODE=") != 1 {
		t.Fatal("nightly must have exactly one authored KVM device-mode rule")
	}
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" || job.Environment != "" || len(job.Permissions) != 0 || job.TimeoutMinutes <= 135 || job.TimeoutMinutes > 360 {
			t.Fatalf("job %s must use a bounded public hosted runner without protected NVIDIA environments", name)
		}
		if job.If != "github.event_name != 'schedule' || github.repository == 'dsx-ai-factory/nvkr'" {
			t.Fatal("nightly schedules must run only upstream while forks retain manual dispatch")
		}
		built, tenant, source, host, deadline, kvm := false, false, false, false, false, false
		nvkrCheckouts, nvkeCheckouts := 0, 0
		for _, step := range job.Steps {
			for _, line := range strings.Split(step.Run, "\n") {
				if strings.Contains(line, "/dev/kvm") && (strings.Contains(line, "chmod") || strings.Contains(line, "setfacl")) {
					t.Fatal("nightly KVM access must use its single udev rule")
				}
			}
			host = host || strings.Contains(step.Run, `test "$RUNNER_ENVIRONMENT" = github-hosted`)
			source = source || (strings.Contains(step.Run, "commit=$(git -C nvke-source rev-parse HEAD)") &&
				strings.Contains(step.Run, `printf 'NVKR_NVKE_E2E_SOURCE_COMMIT=%s\n' "$commit" >> "$GITHUB_ENV"`))
			// Assert the access boundary, not every udev command or its spelling.
			if strings.Contains(step.Run, "(exec 3<>/dev/kvm)") {
				kvm = strings.Contains(step.Run, "udevadm") && strings.Contains(step.Run, `MODE="0660"`)
			}
			// The authored arithmetic leaves ten minutes before the job's hard
			// deadline; Go reserves independent cleanup time inside that deadline.
			deadline = deadline || (strings.Contains(step.Run, "NVKR_JOB_DEADLINE=%s") && strings.Contains(step.Run, fmt.Sprintf("+ %d * 60 ))", job.TimeoutMinutes-10)))
			if step.Uses != "" {
				_, pin, found := strings.Cut(step.Uses, "@")
				if !found || len(pin) != 40 || strings.Trim(pin, "0123456789abcdef") != "" {
					t.Fatalf("nightly action must be pinned by a full commit SHA: %q", step.Uses)
				}
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				if step.With["persist-credentials"] != false {
					t.Fatal("nightly checkouts must not retain credentials")
				}
				if step.With["repository"] == nil {
					nvkrCheckouts++
					if step.With["ref"] != "${{ github.sha }}" {
						t.Fatal("NVKR checkout must use the dispatched commit")
					}
				} else {
					nvkeCheckouts++
					if step.With["repository"] != "dsx-ai-factory/nvke" || step.With["ref"] != "main" || step.With["path"] != "nvke-source" {
						t.Fatal("NVKE checkout must resolve dsx-ai-factory/nvke main into nvke-source")
					}
				}
			}
			if strings.Contains(step.Run, "go test ./internal/nvkedemo -run '^TestEndToEnd$'") {
				guard := "timeout --kill-after=10s 180s go test ./internal/nvkedemo -list '^TestEndToEnd$' | grep -Fx TestEndToEnd"
				guardAt := strings.Index(step.Run, guard)
				guardLines := 0
				for _, line := range strings.Split(step.Run, "\n") {
					if strings.TrimSpace(line) == guard {
						guardLines++
					}
				}
				built = step.Env["NVKR_NVKE_E2E"] == "1" && step.Env["NVKR_NVKE_E2E_CONFIG"] == "demos/nightly-nvke.yaml" && strings.Contains(step.Run, `-timeout "${remaining}s"`) &&
					strings.Contains(step.Run, `-exec "env --chdir \"$GITHUB_WORKSPACE\""`) &&
					strings.Contains(step.Run, "set -euo pipefail") && strings.Contains(step.Run, "(( remaining > 125 * 60 ))") &&
					guardLines == 1 && guardAt >= 0 && guardAt < strings.Index(step.Run, "remaining=$(( NVKR_JOB_DEADLINE")
				tenant = step.Env["NVKR_NVKE_E2E_TENANT"] == "1"
			}

		}
		if !built || !tenant || !source || !host || !deadline || !kvm || nvkrCheckouts != 1 || nvkeCheckouts != 1 {
			t.Fatal("nightly must build the frozen main source and run the native site/tenant Go E2E on an admitted hosted runner")
		}
	}
	for _, forbidden := range []string{"secrets.", "nightly-validate.sh", "nvkr-validation", "updateRefs", "docker push", "oras push", `MODE="0666"`, "chmod 0666 /dev/kvm"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("nightly violates private logging, credential, distribution or ref boundaries through %q", forbidden)
		}
	}
}
