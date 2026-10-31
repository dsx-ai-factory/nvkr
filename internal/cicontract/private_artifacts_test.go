// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type privateStorageWorkflow struct {
	Env  map[string]string `json:"env"`
	Jobs map[string]struct {
		Permissions map[string]string     `json:"permissions"`
		Env         map[string]string     `json:"env"`
		Steps       []storageWorkflowStep `json:"steps"`
	} `json:"jobs"`
}

func readPrivateStorageWorkflow(t *testing.T, name string) privateStorageWorkflow {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var workflow privateStorageWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestAuthoredBinaryHandoffsUsePrivateStorage(t *testing.T) {
	for _, test := range []struct {
		workflow, job, step string
	}{
		{"nvkr-native-toolchains.yml", "measure", "Retain native measurements"},
		{"nvke-os-validation.yaml", "validate-toolchain", "Upload toolchain evidence"},
		{"nvke-os-validation.yaml", "fetch-nvke-source", "Upload one-day NVKE source handoff"},
		{"nvke-os-validation.yaml", "validate-full", "Upload full validation evidence"},
		{"nvke-os-validation.yaml", "validate-full", "Upload retained QCOW2"},
	} {
		t.Run(test.workflow+"/"+test.step, func(t *testing.T) {
			workflow := readPrivateStorageWorkflow(t, test.workflow)
			job := workflow.Jobs[test.job]
			if job.Permissions["packages"] != "" || job.Permissions["contents"] != "read" {
				t.Fatal("private transfer must not grant repository-token package access")
			}
			found := false
			for _, step := range job.Steps {
				if step.Name != test.step {
					continue
				}
				found = true
				if step.Uses != "./.github/nvkr-run/actions/ci-upload" ||
					step.With["nvkr-ci-storage-write-token"] != "${{ secrets.NVKR_CI_STORAGE_WRITE_TOKEN }}" ||
					step.With["include-hidden-files"] != true {
					t.Fatal("binary handoff escaped the generated private transport")
				}
				if strings.Contains(step.If, "always()") && step.With["nvkr-ci-always"] != "true" {
					t.Fatal("private retention lost its explicit failure and cancellation condition")
				}
				if path, ok := step.With["path"].(string); !ok || path == "" || strings.Contains(path, "**") {
					t.Fatal("private transfer requires explicit paths, not a second glob implementation")
				}
			}
			if !found {
				t.Fatal("required private handoff disappeared")
			}
		})
	}
}

func TestAuthoredPrivateConsumersUseReadCredential(t *testing.T) {
	for _, test := range []struct {
		workflow, job, name, artifact, path string
	}{
		{"nvke-os-validation.yaml", "validate-full", "Download exact NVKE source handoff", "nvke-os-source-${{ github.run_id }}", ".trusted-transfer"},
	} {
		t.Run(test.workflow, func(t *testing.T) {
			workflow := readPrivateStorageWorkflow(t, test.workflow)
			job := workflow.Jobs[test.job]
			if job.Permissions["actions"] != "read" {
				t.Fatal("private locator download must have Actions read access")
			}
			index := slices.IndexFunc(job.Steps, func(step storageWorkflowStep) bool { return step.Name == test.name })
			if index < 0 {
				t.Fatal("private download disappeared")
			}
			step := job.Steps[index]
			if step.Uses != "./.github/nvkr-run/actions/ci-download" ||
				step.With["nvkr-ci-storage-read-token"] != "${{ secrets.NVKR_CI_STORAGE_READ_TOKEN }}" ||
				step.With["name"] != test.artifact || step.With["path"] != test.path {
				t.Fatal("private download changed its credential or locator handoff")
			}
		})
	}
}

func TestNightlyDoesNotUploadProductsOrMetadata(t *testing.T) {
	workflow := readPrivateStorageWorkflow(t, "nvkr-nightly.yml")
	for name, job := range workflow.Jobs {
		for permission, level := range job.Permissions {
			if permission == "packages" || permission == "id-token" || level == "write" {
				t.Fatalf("nightly job %s grants publication authority: %s=%s", name, permission, level)
			}
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				t.Fatalf("nightly job %s uploads files from the local build", name)
			}
			for _, action := range []string{"actions/cache@", "docker/build-push-action@", "./.github/nvkr-run/actions/ci-upload"} {
				if strings.HasPrefix(step.Uses, action) {
					t.Fatalf("nightly coordinator %s exports product artifacts through %s", name, step.Uses)
				}
			}
			if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["cache"] != false {
				t.Fatalf("nightly job %s enables shared build cache", name)
			}
		}
	}
}

func TestOSCandidateCannotObtainPrivateWriterOrUploadFiles(t *testing.T) {
	workflow := readPrivateStorageWorkflow(t, "nvke-os-validation.yaml")
	const trusted = "needs.select.outputs.commit == github.workflow_sha"
	for _, name := range []string{"validate-toolchain", "validate-full"} {
		job := workflow.Jobs[name]
		if len(workflow.Env) != 0 || len(job.Env) != 0 || job.Permissions["packages"] != "" {
			t.Fatal("candidate execution must not inherit ambient storage credentials or package permissions")
		}
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				t.Fatal("candidate-controlled files may not enter public artifact storage")
			}
			if step.Uses == "./.github/nvkr-run/actions/ci-upload" {
				if !strings.Contains(step.If, trusted) || strings.Contains(step.If, "||") || step.ContinueOnError != nil {
					t.Fatal("postcandidate private storage requires exact workflow source admission")
				}
			}
			for _, value := range step.Env {
				if strings.Contains(value, "NVKR_CI_STORAGE_WRITE_TOKEN") {
					t.Fatal("private writer credential must remain inside its admitted transfer action")
				}
			}
			for input, value := range step.With {
				if text, ok := value.(string); ok && strings.Contains(text, "NVKR_CI_STORAGE_WRITE_TOKEN") &&
					(step.Uses != "./.github/nvkr-run/actions/ci-upload" || input != "nvkr-ci-storage-write-token") {
					t.Fatal("private writer credential escaped its admitted transfer action")
				}
			}
		}
	}
	steps := workflow.Jobs["validate-full"].Steps
	download := slices.IndexFunc(steps, func(step storageWorkflowStep) bool { return step.Name == "Download exact NVKE source handoff" })
	candidate := slices.IndexFunc(steps, func(step storageWorkflowStep) bool { return step.Name == "Check out the selected NVKR candidate" })
	if download < 0 || candidate <= download {
		t.Fatal("private source credential may only enter the runner before candidate code")
	}
	for _, step := range steps[candidate:] {
		if _, ok := step.With["nvkr-ci-storage-read-token"]; ok {
			t.Fatal("candidate code can receive a later private source credential")
		}
	}
	selection := workflow.Jobs["select"].Steps
	if !slices.ContainsFunc(selection, func(step storageWorkflowStep) bool {
		return step.Name == "Validate selection" && step.Env["WORKFLOW_SHA"] == "${{ github.workflow_sha }}"
	}) || !slices.ContainsFunc(selection, func(step storageWorkflowStep) bool {
		return step.Name == "Summarize selected validation" && strings.Contains(step.Run, "GITHUB_STEP_SUMMARY")
	}) {
		t.Fatal("trusted selection identity and summary must precede candidate jobs")
	}
}
