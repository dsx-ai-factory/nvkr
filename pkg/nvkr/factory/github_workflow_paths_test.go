// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"
)

func TestGeneratedGitHubChildWorkflowNamespace(t *testing.T) {
	for _, test := range []struct {
		path  string
		owner string
	}{
		{".github/workflows/nvkr-run__host-amd64-node.yml", GitHubWorkflowPath},
		{".github/workflows/nvkr-dev__host-arm64-node.yml", GitHubDevelopmentWorkflowPath},
		{".github/workflows/nvkr-run-nightly__host-amd64-node.yml", ".github/workflows/nvkr-run-nightly.yml"},
		{".github/workflows/nvkr-run__" + strings.Repeat("a", 64) + ".yml", GitHubWorkflowPath},
		{".github/workflows/nvkr-run__" + strings.Repeat("a", 65) + ".yml", ""},
		{".github/workflows/nvkr-run-host-amd64-node.yml", ""},
		{".github/workflows/nvkr-run__host.yaml", ""},
		{".github/workflows/nvkr-run__host/step.yml", ""},
		{".github/workflows/nvkr-run__host__step.yml", ""},
		{".github/workflows/nvkr-run__-host.yml", ""},
		{".github/workflows/nvkr-run__host-.yml", ""},
		{".github/workflows/nvkr-run__Host.yml", ""},
		{".github/workflows/nvkr-run-__host.yml", ""},
		{".github/workflows/nvkr-dev-nightly__host.yml", ""},
		{".github/workflows/nvkr-smoke__host.yml", ""},
		{".github/workflows/../workflows/nvkr-run__host.yml", ""},
		{"/.github/workflows/nvkr-run__host.yml", ""},
	} {
		t.Run(test.path, func(t *testing.T) {
			owner, ok := githubChildWorkflowOwner(test.path)
			if ok != (test.owner != "") || (ok && owner != test.owner) || IsGeneratedGitHubChildWorkflowPath(test.path) != ok {
				t.Fatalf("owner = %q, %t; want %q", owner, ok, test.owner)
			}
			if ok && (!githubWorkflowPathInNamespace(owner, test.path) || githubWorkflowPathInNamespace(".github/workflows/nvkr-run-other.yml", test.path)) {
				t.Fatal("child ownership escaped its exact entry namespace")
			}
		})
	}
}
