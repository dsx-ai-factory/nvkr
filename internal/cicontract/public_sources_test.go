// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"strings"
	"testing"
)

func TestAuthoredNVKEFetchesAreAnonymous(t *testing.T) {
	for _, test := range []struct{ workflow, step string }{
		{"merge-gate.yaml", "Fetch public NVKE validation inputs"},
		{"nvke-os-validation.yaml", "Check out exact pinned NVKE source"},
		{"nvkr-native-toolchains.yml", "Fetch public source for catalog and generation checks"},
	} {
		t.Run(test.workflow, func(t *testing.T) {
			found := 0
			for _, job := range readPrivateStorageWorkflow(t, test.workflow).Jobs {
				for _, step := range job.Steps {
					if step.Name != test.step {
						continue
					}
					found++
					if step.Uses != "" || len(step.With) != 0 ||
						step.Env["GIT_CONFIG_NOSYSTEM"] != "1" || step.Env["GIT_CONFIG_GLOBAL"] != "/dev/null" ||
						step.Env["GIT_CONFIG_COUNT"] != "0" || step.Env["GIT_TERMINAL_PROMPT"] != "0" {
						t.Fatal("source fetch must disable ambient Git credentials without a checkout action token")
					}
					for key, value := range step.Env {
						if strings.Contains(value, "secrets.") || strings.Contains(value, "github.token") || strings.Contains(key, "TOKEN") {
							t.Fatal("anonymous source fetch received a credential")
						}
					}
					if !strings.Contains(step.Run, "timeout 300 git") || !strings.Contains(step.Run, "-c credential.helper= fetch ") ||
						!strings.Contains(step.Run, "https://github.com/dsx-ai-factory/nvke \"$NVKE_REF\"") ||
						!strings.Contains(step.Run, "checkout --detach FETCH_HEAD") {
						t.Fatal("anonymous source fetch lost its bound or selected revision")
					}
				}
			}
			if found != 1 {
				t.Fatalf("found %d source fetch steps, want one", found)
			}
		})
	}
}
