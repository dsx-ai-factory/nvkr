// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// Bootstrap runs before the helper exists, so its commands remain literal. Only
// the runner artifact's name varies between calls to the same architecture's
// action. Full action equality also preserves shells, settings and secret scope.
func shareGitHubBootstrap(plan *Plan, files map[string][]byte, oldPath string, call *yamlv3.Node) error {
	if !strings.HasPrefix(oldPath, githubActionsDirectoryFor(plan)+"jobs/") || !strings.HasSuffix(oldPath, "/action.yml") ||
		!safeGeneratedPath(oldPath) || len(files[oldPath]) == 0 || call == nil || call.Kind != yamlv3.MappingNode ||
		githubYAMLValue(call, "uses") != "./"+strings.TrimSuffix(oldPath, "/action.yml") {
		return e.New(e.CodePipelineDrift, "shared bootstrap requires an owned generated action and its exact call")
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(files[oldPath], &document); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decode generated bootstrap action")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yamlv3.MappingNode {
		return e.New(e.CodePipelineDrift, "generated bootstrap action requires a mapping document")
	}
	action := document.Content[0]
	linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return err
	}
	release, err := githubRunnerReleaseForGenerator()
	if err != nil {
		return err
	}
	runs := githubYAMLField(action, "runs")
	steps := githubYAMLField(runs, "steps")
	if githubYAMLValue(runs, "using") != "composite" || steps == nil || steps.Kind != yamlv3.SequenceNode {
		return e.New(e.CodePipelineDrift, "generated bootstrap action requires composite steps")
	}
	variant := ""
	var artifact *yamlv3.Node
	for _, step := range steps.Content {
		switch githubYAMLValue(step, "name") {
		case githubRunnerInstallStep:
			if variant != "" {
				return e.New(e.CodePipelineDrift, "generated bootstrap action repeats its runner build")
			}
			command := strings.TrimSuffix(githubYAMLValue(step, "run"), "\n")
			for _, candidate := range []struct{ name, environment string }{
				{"linux-amd64", "CGO_ENABLED=0 GOOS=linux GOARCH=amd64"},
				{"linux-arm64", "CGO_ENABLED=0 GOOS=linux GOARCH=arm64"},
				{"native", "CGO_ENABLED=0"},
			} {
				if command == githubRunnerInstallCommand(candidate.environment, linkerFlags, release) {
					variant = candidate.name
				}
			}
			if variant == "" {
				return e.New(e.CodeEngineCapability, "generated bootstrap action has an unsupported runner build command")
			}
		case "Upload NVKR runner":
			if artifact != nil || !strings.HasPrefix(githubYAMLValue(step, "uses"), "actions/upload-artifact@") {
				return e.New(e.CodePipelineDrift, "generated bootstrap action requires one pinned runner upload")
			}
			artifact = githubYAMLField(githubYAMLField(step, "with"), "name")
			if artifact == nil || artifact.Kind != yamlv3.ScalarNode || artifact.Value == "" {
				return e.New(e.CodePipelineDrift, "generated bootstrap action requires a runner artifact name")
			}
		}
	}
	if variant == "" || artifact == nil {
		return e.New(e.CodePipelineDrift, "generated bootstrap action omits its runner build or upload")
	}
	inputs := githubYAMLField(action, "inputs")
	arguments := githubYAMLField(call, "with")
	if (inputs != nil && inputs.Kind != yamlv3.MappingNode) || (arguments != nil && arguments.Kind != yamlv3.MappingNode) ||
		githubYAMLField(inputs, "runner-artifact") != nil || githubYAMLField(arguments, "runner-artifact") != nil {
		return e.New(e.CodePipelineDrift, "generated bootstrap action conflicts with the runner-artifact input")
	}
	if inputs == nil {
		inputs = githubYAMLMap()
		githubYAMLSet(action, "inputs", inputs)
	}
	if arguments == nil {
		arguments = githubYAMLMap()
	}
	value := *artifact
	*artifact = *githubYAMLString("${{ inputs.runner-artifact }}")
	githubYAMLSet(inputs, "runner-artifact", githubActionInput("Same-Run helper artifact name"))
	githubYAMLSet(action, "name", githubYAMLString("Build NVKR runner"))
	raw, err := encodeGitHubYAML(&document)
	if err != nil {
		return err
	}
	path := githubActionsDirectoryFor(plan) + "bootstrap-" + variant
	if plan.Environment.UsesGitHubRuntimePlan() {
		for _, step := range steps.Content {
			if githubYAMLValue(step, "name") == "Upload NVKR runtime inputs" {
				path += "-runtime-plan"
			}
		}
	}
	newPath := path + "/action.yml"
	if previous, exists := files[newPath]; exists && !bytes.Equal(previous, raw) {
		return e.New(e.CodeEngineCapability, "generated bootstrap actions for %q have conflicting implementations", variant)
	}
	// No shared state changes until the complete candidate has been admitted.
	githubYAMLSet(arguments, "runner-artifact", &value)
	githubYAMLSet(call, "with", arguments)
	githubYAMLSet(call, "uses", githubYAMLString("./"+path))
	files[newPath] = raw
	delete(files, oldPath)
	return nil
}
