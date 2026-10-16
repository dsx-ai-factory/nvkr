// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestAuthoredBinaryHandoffsUsePrivateStorage(t *testing.T) {
	for _, test := range []struct {
		workflow, job, step string
	}{
		{"nvkr-native-toolchains.yml", "measure", "Retain native measurements"},
		{"nvke-os-validation.yaml", "fetch-nvke-source", "Upload one-day NVKE source handoff"},
		{"nvke-os-validation.yaml", "validate-full", "Upload full validation evidence"},
		{"nvke-os-validation.yaml", "validate-full", "Upload retained QCOW2"},
	} {
		t.Run(test.workflow+"/"+test.step, func(t *testing.T) {
			raw, err := os.ReadFile("../../.github/workflows/" + test.workflow)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Jobs map[string]struct {
					Permissions map[string]string `json:"permissions"`
					Steps       []struct {
						Name string         `json:"name"`
						Uses string         `json:"uses"`
						If   string         `json:"if"`
						With map[string]any `json:"with"`
					} `json:"steps"`
				} `json:"jobs"`
			}
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			job := workflow.Jobs[test.job]
			if job.Permissions["packages"] != "write" || job.Permissions["contents"] != "read" {
				t.Fatal("private upload has no explicit transfer permission")
			}
			found := false
			for _, step := range job.Steps {
				if step.Name != test.step {
					continue
				}
				found = true
				if step.Uses != "./.github/nvkr-run/actions/ci-upload" ||
					step.With["nvkr-ci-storage-token"] != "${{ secrets.NVKR_CI_STORAGE_TOKEN || github.token }}" ||
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
