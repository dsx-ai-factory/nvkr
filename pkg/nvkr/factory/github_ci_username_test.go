// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubCITransferUsesConfiguredCredentialAccount(t *testing.T) {
	plan := githubTestPlan(t)
	files, err := githubCITransferTemplates(plan, testActionRefs(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, username, input string }{
		{"upload", "${{ vars.NVKR_CI_STORAGE_WRITE_USER }}", githubCIStorageInput},
		{"download", "${{ vars.NVKR_CI_STORAGE_READ_USER }}", githubCIReadInput},
		{"bootstrap-download", "${{ vars.NVKR_CI_STORAGE_READ_USER }}", githubCIReadInput},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var document yamlv3.Node
			if err := yamlv3.Unmarshal(files[githubCITransferPath(plan, tc.mode)+"/action.yml"], &document); err != nil {
				t.Fatal(err)
			}
			credentials := 0
			steps := githubTestYAMLField(t, document.Content[0], "runs", "steps")
			for _, step := range steps.Content {
				env := githubYAMLField(step, "env")
				if githubYAMLField(env, "NVKR_CI_STORAGE_TOKEN") == nil {
					continue
				}
				credentials++
				if githubYAMLValue(env, "NVKR_CI_STORAGE_USER") != tc.username ||
					githubYAMLValue(env, "NVKR_CI_STORAGE_TOKEN") != githubCICredential(tc.input, false) ||
					githubYAMLValue(env, "NVKR_CI_REPOSITORY_TOKEN") != "${{ github.token }}" {
					t.Fatal("private transfer substituted the workflow actor or mixed read/write accounts")
				}
			}
			if credentials != 1 {
				t.Fatal("private account must be scoped to one transfer command")
			}
		})
	}
}
