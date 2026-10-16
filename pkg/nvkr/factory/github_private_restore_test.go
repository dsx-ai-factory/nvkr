// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestPrivateStageRestoreProjectionPreservesItsExactOperation(t *testing.T) {
	for _, family := range []string{"execute-node", "execute-source", "prepare-inputs", "execute-node-container", "prepare-inputs-container"} {
		t.Run(family, func(t *testing.T) {
			plan := &Plan{}
			name := githubActionsDirectoryFor(plan) + "shared/" + family + "/action.yml"
			restore := githubCIStageRestoreStep(plan, name)
			download := githubYAMLMap("id", githubYAMLString("operation_download"), "uses", githubYAMLString("./"+githubCITransferPath(plan, "download")),
				"with", githubYAMLMap("path", githubYAMLString(".nvkr/carriers")))
			steps := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: []*yamlv3.Node{download, restore}}
			document := &yamlv3.Node{Kind: yamlv3.DocumentNode, Content: []*yamlv3.Node{githubYAMLMap("runs", githubYAMLMap("steps", steps))}}
			before, err := encodeGitHubYAML(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := projectGitHubPrivateRestore(plan, name, document, false); err != nil {
				t.Fatal(err)
			}
			if len(steps.Content) != 1 || githubYAMLValue(githubYAMLField(download, "with"), githubCIRestoreStageInput) != githubParameterExpression("restore_selector") {
				t.Fatal("stage still materializes its carrier or lost its exact selector")
			}
			if err := projectGitHubPrivateRestore(plan, name, document, true); err != nil {
				t.Fatal(err)
			}
			after, err := encodeGitHubYAML(document)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("private projection changed the logical operation", err)
			}
		})
	}
}

func TestPrivateStageRestoreRejectsChangedSelectorsAndCommands(t *testing.T) {
	for _, changed := range []string{"command", "credential", "selector", "missing"} {
		t.Run(changed, func(t *testing.T) {
			plan := &Plan{}
			name := githubActionsDirectoryFor(plan) + "shared/execute-node/action.yml"
			restore := githubCIStageRestoreStep(plan, name)
			download := githubYAMLMap("id", githubYAMLString("operation_download"), "uses", githubYAMLString("./"+githubCITransferPath(plan, "download")), "with", githubYAMLMap())
			steps := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: []*yamlv3.Node{download, restore}}
			document := &yamlv3.Node{Kind: yamlv3.DocumentNode, Content: []*yamlv3.Node{githubYAMLMap("runs", githubYAMLMap("steps", steps))}}
			inverse := false
			switch changed {
			case "command":
				githubYAMLSet(restore, "run", githubYAMLString("arbitrary command"))
			case "credential":
				githubYAMLSet(githubYAMLField(restore, "env"), "NVKR_CI_STORAGE_TOKEN", githubYAMLString("unscoped"))
			case "selector":
				inverse = true
				steps.Content = steps.Content[:1]
				githubYAMLSet(githubYAMLField(download, "with"), githubCIRestoreStageInput, githubYAMLString("another-stage"))
			case "missing":
				steps.Content = steps.Content[:1]
			}
			if err := projectGitHubPrivateRestore(plan, name, document, inverse); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("changed operation admitted: %v", err)
			}
		})
	}
}
