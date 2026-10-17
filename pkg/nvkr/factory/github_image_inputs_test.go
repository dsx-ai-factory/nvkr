// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestPrivateImageCallerContextsAcrossPublicationPolicies(t *testing.T) {
	base, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{ProductionRegistryWriteCredential, "github:GITHUB_TOKEN"} {
		t.Run(credential, func(t *testing.T) {
			environment := base.Environment.Clone()
			environment.Publication.CredentialRef = credential
			stack := &catalog.Stack{Catalog: base.CatalogSnapshot, Digest: base.CatalogDigest, Applied: base.AppliedOverlays}
			stacks := map[string]*catalog.Stack{}
			for _, output := range base.Definition.Outputs {
				if output.Catalog == nil {
					continue
				}
				for _, build := range base.Builds {
					if build.Name == nativeOutputSelections(output)[0].Name {
						stacks[output.Name] = &catalog.Stack{Catalog: build.CatalogSnapshot, Digest: build.CatalogDigest, Applied: build.AppliedOverlays}
						break
					}
				}
				if stacks[output.Name] == nil {
					t.Fatalf("missing catalog snapshot for %s", output.Name)
				}
			}
			plan, err := compileWithTargetStacks(t.Context(), base.Definition, environment, stack, stacks,
				base.GeneratorVersion, base.DefinitionPath, base.EnvironmentPath)
			if err != nil {
				t.Fatal(err)
			}
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
