// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
			Group  string `json:"group"`
			Cancel bool   `json:"cancel-in-progress"`
		} `json:"concurrency"`
		Jobs map[string]struct {
			If          string            `json:"if"`
			Needs       any               `json:"needs"`
			Environment string            `json:"environment"`
			Timeout     int               `json:"timeout-minutes"`
			Permissions map[string]string `json:"permissions"`
			Outputs     map[string]string `json:"outputs"`
			Steps       []struct {
				ID   string            `json:"id"`
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
	if workflow.Permissions["contents"] != "read" || workflow.Permissions["actions"] != "read" || workflow.Concurrency.Cancel || workflow.Concurrency.Group != "native-toolchains-${{ github.ref }}" {
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
	report := workflow.Jobs["report"]
	if !strings.Contains(report.If, "always()") || !strings.Contains(report.If, "github.ref == 'refs/heads/main'") ||
		!maps.Equal(report.Permissions, map[string]string{"contents": "read", "issues": "write"}) {
		t.Fatal("refresh reporter lost failure scheduling, serialized main admission or minimal permissions")
	}
	notify := report.Steps[len(report.Steps)-1]
	if notify.Run != "python3 scripts/report-native-toolchains.py report" || notify.Env["RESULTS"] != "${{ toJSON(needs) }}" {
		t.Fatal("refresh reporter bypasses tested actionable-issue routing")
	}
	measure := workflow.Jobs["measure"]
	for _, arch := range []string{"amd64", "arm64"} {
		key := "changed_" + arch
		if measure.Outputs[key] != "${{ steps.compare.outputs."+key+" }}" {
			t.Fatal("matrix comparisons must have distinct outputs for both architectures")
		}
	}
	compares := false
	for _, step := range measure.Steps {
		if step.ID == "compare" && strings.Contains(step.Run, "report-native-toolchains.py compare") &&
			step.Env["NATIVE_ARCH"] == "${{ matrix.arch }}" {
			compares = true
		}
	}
	if !compares {
		t.Fatal("measurement must compare actual proposal manifests with committed pins")
	}
}

func TestNativeToolchainPushPaths(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, Workflow))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			Push struct {
				Paths []string `json:"paths"`
			} `json:"push"`
		} `json:"on"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, path := range []string{
		".go-version", "go.mod", "go.sum", "toolchains/nvkr-packer/**", "toolchains/nvkr-disk-inspect/**",
		"scripts/prepare-native-toolchains.sh", "scripts/verify-native-toolchains.sh", Workflow,
	} {
		want[path] = true
	}
	// Match the static Linux helper build in prepare-native-toolchains.sh, not
	// the test binary's host platform or the much larger maintenance CLI closure.
	for _, arch := range []string{"amd64", "arm64"} {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", "-f",
			"{{if and .Module .Module.Main}}{{.Dir}}{{end}}", "./cmd/nvkr-disk-inspect")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		out, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatalf("list native %s dependencies: %v", arch, err)
		}
		for _, dir := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			rel, err := filepath.Rel(root, dir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("module-local dependency outside repository: %q (%v)", dir, err)
			}
			want[filepath.ToSlash(rel)+"/**"] = true
		}
	}
	paths := workflow.On.Push.Paths
	slices.Sort(paths)
	expected := slices.Sorted(maps.Keys(want))
	if !slices.Equal(paths, expected) {
		t.Fatalf("native image push paths drifted from Linux helper dependencies; update %s\ngot: %v\nwant: %v", Workflow, paths, expected)
	}
}
