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
	var dispatch struct {
		Inputs map[string]struct {
			Type    string
			Default *bool
		}
	}
	if err := json.Unmarshal(workflow.On["workflow_dispatch"], &dispatch); err != nil {
		t.Fatal(err)
	}
	debug := dispatch.Inputs["debug_redacted_kubeadm_error"]
	if debug.Type != "boolean" || debug.Default == nil || *debug.Default {
		t.Fatal("temporary redacted terminal output must require explicit boolean opt-in")
	}
	if workflow.Concurrency.Group != "nvkr-nightly-${{ github.ref }}" || workflow.Concurrency.CancelInProgress == nil || *workflow.Concurrency.CancelInProgress {
		t.Fatal("nightly must serialize each branch without canceling its owned cleanup")
	}
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" || job.Environment != "" || len(job.Permissions) != 0 || job.TimeoutMinutes <= 135 || job.TimeoutMinutes > 360 {
			t.Fatalf("job %s must use a bounded public hosted runner without protected NVIDIA environments", name)
		}
		if job.If != "github.event_name != 'schedule' || github.repository == 'dsx-ai-factory/nvkr'" {
			t.Fatal("nightly schedules must run only upstream while forks retain manual dispatch")
		}
		built, tenant, source, host, deadline, kvm := false, false, false, false, false, false
		nvkrCheckouts := 0
		for _, step := range job.Steps {
			host = host || strings.Contains(step.Run, `test "$RUNNER_ENVIRONMENT" = github-hosted`)
			source = source || strings.Contains(step.Run, "NVKR_NVKE_E2E_SOURCE_COMMIT=")
			if strings.Contains(step.Run, "99-nvkr-kvm.rules") {
				kvm = true
				for _, required := range []string{
					`KERNEL=="kvm", OWNER="%s", GROUP="kvm", MODE="0660"`, `"$(id -u)"`,
					"udevadm verify /etc/udev/rules.d/99-nvkr-kvm.rules", "udevadm control --reload-rules",
					"udevadm trigger --name-match=kvm", "udevadm settle --timeout=25", "(exec 3<>/dev/kvm)",
				} {
					if !strings.Contains(step.Run, required) {
						t.Fatalf("nightly must retain persistent runner-scoped KVM access and an unprivileged probe: missing %q", required)
					}
				}
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
				}
			}
			if strings.Contains(step.Run, "go test ./internal/nvkedemo -run '^TestEndToEnd$'") {
				guard := "timeout --kill-after=10s 180s go test ./internal/nvkedemo -list '^TestEndToEnd$' | grep -Fx TestEndToEnd"
				guardAt := strings.Index(step.Run, guard)
				built = step.Env["NVKR_NVKE_E2E"] == "1" && strings.Contains(step.Run, `-timeout "${remaining}s"`) &&
					strings.Contains(step.Run, "set -euo pipefail") && strings.Contains(step.Run, "(( remaining > 125 * 60 ))") &&
					guardAt >= 0 && guardAt < strings.Index(step.Run, "remaining=$(( NVKR_JOB_DEADLINE")
				tenant = step.Env["NVKR_NVKE_E2E_TENANT"] == "1"
				if step.Env["NVKR_NVKE_E2E_DEBUG_REDACTED_ERROR"] != "${{ github.event_name == 'workflow_dispatch' && inputs.debug_redacted_kubeadm_error && '1' || '0' }}" {
					t.Fatal("scheduled runs must keep terminal errors private")
				}
			}
			if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["cache"] != false {
				t.Fatal("nightly must not publish build outputs through the Go Actions cache")
			}
			if strings.HasPrefix(step.Uses, "docker/setup-buildx-action@") && step.With["cache-binary"] != false {
				t.Fatal("nightly must not enable Buildx binary caching")
			}
		}
		if !built || !tenant || !source || !host || !deadline || !kvm || nvkrCheckouts != 1 {
			t.Fatal("nightly must build the frozen main source and run the native site/tenant Go E2E on an admitted hosted runner")
		}
	}
	for _, forbidden := range []string{"secrets.", "actions/upload-artifact@", "actions/cache@", "nightly-validate.sh", "nvkr-validation", "updateRefs", "docker push", "oras push", `MODE="0666"`, "chmod 0666 /dev/kvm"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("nightly introduces credentials, distribution or ref mutation through %q", forbidden)
		}
	}
}
