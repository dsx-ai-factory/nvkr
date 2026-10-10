// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestGitHubNodeContextIncludesExactBindingsWithoutCredentials(t *testing.T) {
	plan := &Plan{Builds: []BuildPlan{{Name: "image", Graph: &ir.Graph{Nodes: []ir.Node{{
		ID: "build/controller", Component: "controller", BuilderType: "oci-hermetic", SourceCommit: strings.Repeat("a", 40),
		Command: []string{"tool", "--password=do-not-log-this"}, Env: []ir.EnvVar{{Name: "TOKEN", Value: "do-not-log-this"}}, Repo: "https://do-not-log-this@example.com/source",
	}}}}}, Graph: ArtifactGraph{Artifacts: []Artifact{
		{ID: "source/controller", Kind: "source-closure", Producer: "fetch/controller"},
		{ID: "artifact/controller", Kind: "oci-image", Name: "controller", Producer: "build/controller"},
	}}}
	node := Node{ID: "build/image/controller", Kind: NodeBuild, IRNode: "build/controller", BuildTarget: "image", Platform: "linux/arm64", Network: "none", Inputs: []string{"source/controller"}, Outputs: []string{"artifact/controller"}}
	text := strings.Join(githubNodeDetails(plan, node), "\n")
	for _, want := range []string{"Node: build/image/controller", "Selected output: image", "Component: controller", "Producer instruction: build/controller", "Platform: linux/arm64", "Input: source/controller", "Expected output: artifact/controller", "producer build/controller"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "do-not-log-this") || strings.Contains(strings.Join(githubNodeCommands(node, plan), "\n"), "do-not-log-this") {
		t.Fatal("raw arguments, environment or source credentials entered workflow diagnostics")
	}
}
