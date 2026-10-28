// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// One native disk and its vendor fetch expose both image-preparation call sites.
// Publication membership and per-output catalog preservation have their own
// compiler contracts; credential projection does not require the full product.
func privateImageTestPlan(t *testing.T, credential string) *Plan {
	t.Helper()
	return recompileGitHubStabilityPlan(t, arcTestPlan(t, "linux/amd64"), func(inputs *githubStabilityInputs) {
		inputs.environment.Publication.RepositoryPrefix = "ghcr.io/example/nvkr"
		inputs.environment.Publication.CredentialRef = credential
		inputs.environment.Publication.Anonymous = false
		root := inputs.catalog.Components["image"]
		root.VendorCommand = []string{"make", "vendor"}
		for arch, image := range root.ToolchainImages {
			root.ToolchainImages[arch] = "ghcr.io/example/nvkr/toolchains/packer-" + arch + image[strings.Index(image, "@sha256:"):]
		}
		inputs.catalog.Components["image"] = root
	})
}

func TestPrivateImageCallerContextsAcrossPublicationPolicies(t *testing.T) {
	for _, credential := range []string{ProductionRegistryWriteCredential, "github:GITHUB_TOKEN"} {
		t.Run(credential, func(t *testing.T) {
			plan := privateImageTestPlan(t, credential)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			// The pinned runner action_yaml.json step-env context excludes vars
			// and secrets. Job/workflow contexts must be resolved by the caller.
			for path, raw := range bundle.Files {
				if strings.HasSuffix(path, "/action.yml") && (bytes.Contains(raw, []byte("vars.")) || bytes.Contains(raw, []byte("secrets."))) {
					t.Fatalf("caller-only context leaked into composite metadata %s", path)
				}
			}
			calls := 0
			for _, job := range decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs {
				for _, step := range job.Steps {
					if _, ok := step.With["nvkr-toolchain-read-token"]; ok {
						calls++
						if step.With["nvkr-toolchain-read-token"] != "${{ secrets.NVKR_REGISTRY_READ }}" || step.With["nvkr-toolchain-read-user"] != "${{ vars.NVKR_REGISTRY_READ_USER }}" {
							t.Fatal("image caller lost explicit account/token bindings")
						}
					}
				}
			}
			if calls == 0 {
				t.Fatal("fixture has no private image calls")
			}
		})
	}
}

func TestAbsentImageReaderDefaultsFailClosed(t *testing.T) {
	for _, inputName := range []string{"nvkr-toolchain-read-token", "nvkr-toolchain-read-user"} {
		for _, change := range []string{"unchanged", "caller argument", "required", "missing default", "nonempty default"} {
			t.Run(inputName+"/"+change, func(t *testing.T) {
				inputs, arguments := githubYAMLMap(), githubYAMLMap()
				for _, name := range []string{"nvkr-toolchain-read-token", "nvkr-toolchain-read-user"} {
					githubYAMLSet(inputs, name, githubYAMLMap("required", githubYAMLString("false"), "default", githubYAMLString("")))
				}
				input := githubYAMLField(inputs, inputName)
				switch change {
				case "caller argument":
					githubYAMLSet(arguments, inputName, githubYAMLString("unexpected-reader"))
				case "required":
					githubYAMLSet(input, "required", githubYAMLString("true"))
				case "missing default":
					githubYAMLRemove(input, "default")
				case "nonempty default":
					githubYAMLSet(input, "default", githubYAMLString("unexpected-reader"))
				}
				steps := &yamlv3.Node{Kind: yamlv3.SequenceNode, Content: []*yamlv3.Node{githubYAMLMap("id", githubYAMLString("operation_images"), "if", githubYAMLString("${{ fromJSON(inputs.parameters).images_present == 'true' }}"))}}
				err := materializeGitHubInterfaceSteps(steps, map[string]string{"images_present": "false"}, inputs, arguments)
				if change != "unchanged" {
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(steps.Content) != 0 || len(inputs.Content) != 0 {
					t.Fatal("absent image role retained credential surface")
				}
			})
		}
	}
}

func TestPrivateImageProjectionRejectsCallerCredentialSubstitution(t *testing.T) {
	for _, input := range []string{"nvkr-toolchain-read-token", "nvkr-toolchain-read-user"} {
		for _, change := range []string{"valid", "missing", "empty", "substituted"} {
			t.Run(input+"/"+change, func(t *testing.T) {
				plan := &Plan{}
				env := githubYAMLMap(githubImageToken, githubYAMLString("${{ inputs.nvkr-toolchain-read-token }}"), githubImageUser, githubYAMLString("${{ inputs.nvkr-toolchain-read-user }}"))
				step := githubYAMLMap("name", githubYAMLString("Prepare images"), "run", githubYAMLString(githubRunnerCommand(plan, "prepare-images", "stage", "private-stage")), "env", env)
				arguments := githubYAMLMap("nvkr-toolchain-read-token", githubYAMLString("${{ secrets.NVKR_REGISTRY_READ }}"), "nvkr-toolchain-read-user", githubYAMLString("${{ vars.NVKR_REGISTRY_READ_USER }}"))
				switch change {
				case "missing":
					githubYAMLRemove(arguments, input)
				case "empty":
					githubYAMLSet(arguments, input, githubYAMLString(""))
				case "substituted":
					githubYAMLSet(arguments, input, githubYAMLString("different-nonempty-binding"))
				}
				call := githubYAMLMap("with", arguments)
				invocation := githubActionInvocation{Values: map[string]string{}, Arguments: githubYAMLMap()}
				_, err := githubProjectInterfaceRole(plan, nil, call, githubActionRefs{}, &invocation, githubYAMLMap(), githubInterfaceRole{name: "images", step: step, kind: "prepare-images"}, "bash -e {0}")
				if change == "valid" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if !strings.Contains(err.Error(), "semantic image preparation changes its caller credential") {
					t.Fatalf("wrong refusal boundary: %v", err)
				}
			})
		}
	}
}
