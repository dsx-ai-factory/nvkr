// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestNightlyFreezesAuthenticatedNVKEMainOnce(t *testing.T) {
	selection := nightlyCoordinatorFixture(t).Jobs["select"]
	var resolver nightlyCoordinatorStep
	for _, step := range selection.Steps {
		if step.Name == "Resolve NVKE main once for this nightly cycle" {
			resolver = step
		}
	}
	if selection.Outputs["nvke_commit"] != "${{ steps.source.outputs.nvke_commit }}" || resolver.Env["GH_TOKEN"] != "${{ secrets.NKE_SOURCE_TOKEN }}" {
		t.Fatal("source resolution lost its authenticated bounded owner")
	}
	const commit = "dddddddddddddddddddddddddddddddddddddddd"
	for _, test := range []struct {
		name, kind, sha string
		apiOK, valid    bool
	}{
		{"exact commit", "commit", commit, true, true},
		{"mutable source", "commit", "main", true, false},
		{"tag object", "tag", commit, true, false},
		{"API failure", "commit", commit, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			timeout, err := exec.LookPath("timeout")
			if err != nil {
				timeout, err = exec.LookPath("gtimeout")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(timeout, filepath.Join(root, "timeout")); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"type": test.kind, "sha": test.sha}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "response.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			stub := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >>\"$SOURCE_FIXTURE/calls\"\n[ \"$SOURCE_API_OK\" = true ]\ncat \"$SOURCE_FIXTURE/response.json\"\n"
			if err := os.WriteFile(filepath.Join(root, "gh"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", resolver.Run)
			command.Env = []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"), "GH_TOKEN=synthetic-source-read", "SOURCE_FIXTURE=" + root, "SOURCE_API_OK=" + map[bool]string{true: "true", false: "false"}[test.apiOK], "GITHUB_OUTPUT=" + output}
			log, runErr := command.CombinedOutput()
			raw, readErr := os.ReadFile(output)
			if test.valid {
				if runErr != nil || readErr != nil || string(raw) != "nvke_commit="+commit+"\n" {
					t.Fatalf("exact source not frozen: %v %v %s %s", runErr, readErr, raw, log)
				}
			} else if runErr == nil || !os.IsNotExist(readErr) {
				t.Fatalf("rejected source emitted output: %v %v %s", runErr, readErr, raw)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 1 || !strings.Contains(string(calls), "repos/dsx-ai-factory/nvke/git/ref/heads/main") {
				t.Fatalf("source resolution retried or used a different source: %s %v", calls, err)
			}
		})
	}
}

type nightlyCoordinatorStep struct {
	Name string            `json:"name"`
	Run  string            `json:"run"`
	Uses string            `json:"uses"`
	Env  map[string]string `json:"env"`
	With map[string]any    `json:"with"`
}
type nightlyCoordinatorJob struct {
	Outputs        map[string]string        `json:"outputs"`
	Needs          []string                 `json:"needs"`
	TimeoutMinutes int                      `json:"timeout-minutes"`
	Permissions    map[string]string        `json:"permissions"`
	Steps          []nightlyCoordinatorStep `json:"steps"`
}
type nightlyCoordinatorDocument struct {
	On          map[string]json.RawMessage       `json:"on"`
	Permissions map[string]string                `json:"permissions"`
	Jobs        map[string]nightlyCoordinatorJob `json:"jobs"`
	Concurrency struct {
		CancelInProgress bool `json:"cancel-in-progress"`
	} `json:"concurrency"`
}

func nightlyCoordinatorFixture(t *testing.T) nightlyCoordinatorDocument {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow nightlyCoordinatorDocument
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}
func TestNightlyDispatchesGeneratedProducerBeforeNativeConsumer(t *testing.T) {
	workflow := nightlyCoordinatorFixture(t)
	var schedule []struct {
		Cron string `json:"cron"`
	}
	if err := json.Unmarshal(workflow.On["schedule"], &schedule); err != nil || len(schedule) != 1 || schedule[0].Cron != "17 3 * * *" || workflow.On["workflow_dispatch"] == nil {
		t.Fatalf("daily/manual trigger changed: %+v %v", schedule, err)
	}
	if len(workflow.On) != 2 || len(workflow.Jobs) != 4 || workflow.Concurrency.CancelInProgress || !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("nightly lost its bounded generated-producer/consumer orchestration")
	}
	for name, job := range workflow.Jobs {
		if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > 360 {
			t.Fatalf("job %s has no bounded deadline", name)
		}
	}
	build := workflow.Jobs["build"]
	if !reflect.DeepEqual(build.Needs, []string{"select"}) || build.Permissions["actions"] != "write" {
		t.Fatal("producer dispatch lost selection or dispatch permission")
	}
	dispatched, sourceBound := false, false
	for _, step := range build.Steps {
		dispatched = dispatched || strings.Contains(step.Run, "bash scripts/nightly-build.sh")
		sourceBound = sourceBound || step.Env["NVKE_COMMIT"] == "${{ needs.select.outputs.nvke_commit }}"
		if strings.Contains(step.Run, "run local") || strings.Contains(step.Run, "TestEndToEnd") {
			t.Fatal("coordinator rebuilt products instead of dispatching the generated producer")
		}
	}
	runtime := workflow.Jobs["runtime-test"]
	if !dispatched || !sourceBound || !reflect.DeepEqual(runtime.Needs, []string{"select-runtime"}) || runtime.Permissions["actions"] != "read" {
		t.Fatal("consumer omitted exact completed-producer selection")
	}
	ran, deadline := false, false
	for _, step := range runtime.Steps {
		if step.With["repository"] == "dsx-ai-factory/nvke" {
			t.Fatal("artifact consumer must use the source-owned installer, not a second NVKE checkout")
		}
		if nightlyRuntimeDownloadsPrivateInputs(step) {
			t.Fatal("the Go handoff owner restores and authenticates private inputs; do not add a redundant workflow download")
		}
		deadline = deadline || strings.Contains(step.Run, "355 * 60") && strings.Contains(step.Run, "NVKR_JOB_DEADLINE")
		if strings.Contains(step.Run, "go test ./internal/nvkedemo -run") {
			ran = strings.Contains(step.Run, "^TestNightlyPublishedCandidate$") && strings.Contains(step.Run, "-timeout \"${remaining}s\"") && strings.Contains(step.Run, "remaining > 125 * 60")
			if step.Env["NVKR_NIGHTLY_ENABLE"] != "1" || step.Env["NVKR_NIGHTLY_BUILD"] == "" || step.Env["NVKR_NIGHTLY_NETWORK_HELPER"] == "" {
				t.Fatal("consumer omitted authenticated producing Run or existing network helper")
			}
			if step.Env["NVKR_CI_STORAGE_TOKEN"] != "${{ secrets.NVKR_CI_STORAGE_READ_TOKEN }}" || step.Env["NVKR_CI_STORAGE_USER"] != "${{ vars.NVKR_CI_STORAGE_READ_USER }}" {
				t.Fatal("Go handoff owner requires the independent private storage reader")
			}
		}
		if strings.Contains(step.Run, "run local") || strings.Contains(step.Run, "^TestEndToEnd$") {
			t.Fatal("runtime job must consume artifacts without rebuilding NVKE")
		}
	}
	if !ran || !deadline {
		t.Fatal("consumer lost exact private frozen inputs, direct Go test or owned cleanup reserve")
	}
}

func nightlyRuntimeDownloadsPrivateInputs(step nightlyCoordinatorStep) bool {
	if _, forwardsReader := step.With["nvkr-ci-storage-read-token"]; forwardsReader {
		return true
	}
	namespace, action, found := strings.Cut(path.Clean(step.Uses), "/actions/")
	bundle := path.Base(namespace)
	if !found || path.Dir(namespace) != ".github" || (bundle != "nvkr-run" && bundle != "nvkr-dev" && !strings.HasPrefix(bundle, "nvkr-run-")) {
		return false
	}
	switch action {
	case "ci-download", "ci-bootstrap-download", "shared/download-carriers":
		return true
	default:
		return false
	}
}

func TestNightlyRuntimePrivateDownloadGuard(t *testing.T) {
	for _, uses := range []string{
		"./.github/nvkr-run/actions/ci-download",
		"./.github/nvkr-run/actions/ci-bootstrap-download",
		"./.github/nvkr-run/actions/shared/download-carriers",
		"./.github/nvkr-run-nvke-nightly-arm64/actions/ci-download",
		"./.github/nvkr-run-nvke-nightly-arm64/actions/ci-bootstrap-download",
		"./.github/nvkr-run-nvke-nightly-arm64/actions/shared/download-carriers",
		"./.github/nvkr-dev/actions/shared/download-carriers",
	} {
		for _, suffix := range []string{"", "/"} {
			t.Run(uses+suffix, func(t *testing.T) {
				if !nightlyRuntimeDownloadsPrivateInputs(nightlyCoordinatorStep{Uses: uses + suffix}) {
					t.Fatal("generated private download escaped the runtime guard")
				}
			})
		}
	}
	for _, test := range []struct {
		name    string
		step    nightlyCoordinatorStep
		blocked bool
	}{
		{"forwarded reader", nightlyCoordinatorStep{Uses: "./.github/other/action", With: map[string]any{"nvkr-ci-storage-read-token": "synthetic-read-token"}}, true},
		{"empty reader", nightlyCoordinatorStep{Uses: "./.github/other/action", With: map[string]any{"nvkr-ci-storage-read-token": ""}}, true},
		{"null reader", nightlyCoordinatorStep{With: map[string]any{"nvkr-ci-storage-read-token": nil}}, true},
		{"metadata download", nightlyCoordinatorStep{Uses: "actions/download-artifact@pinned-fixture"}, false},
		{"upload interface", nightlyCoordinatorStep{Uses: "./.github/nvkr-run/actions/ci-upload"}, false},
		{"toolchain reader", nightlyCoordinatorStep{Uses: "./.github/nvkr-run/actions/shared/prepare-images", With: map[string]any{"nvkr-toolchain-read-token": "synthetic-read-token"}}, false},
		{"Go handoff owner", nightlyCoordinatorStep{Run: "go test ./internal/nvkedemo", Env: map[string]string{"NVKR_CI_STORAGE_TOKEN": "synthetic-read-token"}}, false},
		{"unrelated download action", nightlyCoordinatorStep{Uses: "./.github/other/actions/ci-download"}, false},
		{"namespace lookalike", nightlyCoordinatorStep{Uses: "./.github/nvkr-runner/actions/ci-download"}, false},
		{"nested directory", nightlyCoordinatorStep{Uses: "./.github/nvkr-run-extra/nested/actions/ci-download"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nightlyRuntimeDownloadsPrivateInputs(test.step); got != test.blocked {
				t.Fatalf("blocked=%v, want %v", got, test.blocked)
			}
		})
	}
}

func TestNightlySelectionExecutesOnlySupportedNVIDIAConsumer(t *testing.T) {
	workflow := nightlyCoordinatorFixture(t)
	var script string
	for _, step := range workflow.Jobs["select"].Steps {
		if step.Name == "Select maintained nightly projects" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing profile selection")
	}
	for _, row := range []struct {
		profile string
		valid   bool
	}{{"all", true}, {"nvke-nightly-arm64", true}, {"nvke-nightly-upstream-ovn-arm64", false}, {"foreign", false}} {
		t.Run(row.profile, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "outputs")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", script)
			command.Dir = "../.."
			command.Env = append(os.Environ(), "PROFILE="+row.profile, "RUNNER_TEMP="+root, "GITHUB_OUTPUT="+output)
			log, err := command.CombinedOutput()
			raw, readErr := os.ReadFile(output)
			if !row.valid {
				if err == nil || !os.IsNotExist(readErr) {
					t.Fatalf("unsupported profile emitted matrix: %v %v %s %s", err, readErr, raw, log)
				}
				return
			}
			if err != nil || readErr != nil {
				t.Fatalf("selection failed: %v %v %s", err, readErr, log)
			}
			var matrix struct {
				Include []struct {
					Name string `json:"name"`
				} `json:"include"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(string(raw), "matrix="))), &matrix); err != nil || len(matrix.Include) != 1 || matrix.Include[0].Name != "nvke-nightly-arm64" {
				t.Fatalf("unexpected selection: %s %v", raw, err)
			}
		})
	}
}
