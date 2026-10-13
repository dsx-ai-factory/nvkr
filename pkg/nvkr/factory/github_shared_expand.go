// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// Materialize only the generator's closed parameter projection for static
// admission. Runtime obtains the same values by rendering the admitted Plan.
// Exact all-member comparison also covers the descriptor and preparation code.
func materializeGitHubSharedActions(raw []byte, files map[string][]byte) ([]byte, map[string][]byte, error) {
	if !strings.Contains(string(raw), "./"+githubSharedDirectory) {
		return raw, files, nil
	}
	var data githubActionDataDocument
	if err := canonical.UnmarshalExact(files[GitHubActionDataPath], &data); err != nil {
		return nil, nil, e.Wrap(e.CodePipelineDrift, err, "decode shared GitHub action data")
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, nil, e.New(e.CodePipelineDrift, "invalid shared GitHub workflow")
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode {
		return nil, nil, e.New(e.CodePipelineDrift, "shared GitHub workflow requires jobs")
	}
	materialized := maps.Clone(files)
	for index := 0; index < len(jobs.Content); index += 2 {
		jobID := jobs.Content[index].Value
		steps := githubYAMLField(jobs.Content[index+1], "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode {
			return nil, nil, e.New(e.CodePipelineDrift, "shared GitHub job requires steps")
		}
		prepared := false
		for pos, call := range steps.Content {
			if call.Kind == yamlv3.AliasNode {
				call = call.Alias
			}
			uses := githubYAMLValue(call, "uses")
			if !strings.HasPrefix(uses, "./"+githubSharedDirectory) {
				continue
			}
			entry, exists := data.Entries[jobID]
			if !exists || entry.Template != "stage-context" || entry.Values == nil {
				return nil, nil, e.New(e.CodePipelineDrift, "job %q has no admitted action context", jobID)
			}
			path := strings.TrimPrefix(uses, "./") + "/action.yml"
			if !safeGeneratedPath(path) || len(files[path]) == 0 {
				return nil, nil, e.New(e.CodePipelineDrift, "shared action %q is missing", path)
			}
			var action yamlv3.Node
			if err := yamlv3.Unmarshal(files[path], &action); err != nil || len(action.Content) != 1 {
				return nil, nil, e.New(e.CodePipelineDrift, "shared action %q is invalid", path)
			}
			body := action.Content[0]
			inner := githubYAMLField(githubYAMLField(body, "runs"), "steps")
			if githubYAMLValue(githubYAMLField(body, "runs"), "using") != "composite" || inner == nil || inner.Kind != yamlv3.SequenceNode {
				return nil, nil, e.New(e.CodePipelineDrift, "shared action %q requires composite steps", path)
			}
			with := githubYAMLField(call, "with")
			if uses == "./"+githubSharedDirectory+"prepare-stage" || uses == "./"+githubSharedDirectory+"prepare-stage-container" {
				if prepared || githubYAMLValue(call, "id") != githubContextStepID || githubYAMLValue(with, "action") != "${{ github.job }}" ||
					len(inner.Content) != 2 || githubYAMLValue(inner.Content[1], "run") != githubRunnerCommand("describe-action", "", "")+` --action "$NVKR_ACTION"` {
					return nil, nil, e.New(e.CodeEngineCapability, "job %q changes shared action preparation", jobID)
				}
				artifact := githubYAMLField(with, "runner-artifact")
				if artifact == nil {
					return nil, nil, e.New(e.CodeEngineCapability, "job %q omits its helper artifact", jobID)
				}
				githubYAMLSubstitute(inner.Content[0], "${{ inputs.runner-artifact }}", artifact)
				steps.Content[pos] = inner.Content[0]
				prepared = true
				continue
			}
			if !prepared {
				return nil, nil, e.New(e.CodeEngineCapability, "job %q uses action data before preparation", jobID)
			}
			inputs := githubYAMLField(body, "inputs")
			if inputs == nil || inputs.Kind != yamlv3.MappingNode || githubYAMLField(inputs, "parameters") == nil || with == nil || with.Kind != yamlv3.MappingNode {
				return nil, nil, e.New(e.CodeEngineCapability, "shared action %q omits its parameter interface", path)
			}
			binding := githubYAMLValue(with, "parameters")
			prefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
			part := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
			if binding != githubParametersExpression(part) || !githubActionPart(part) {
				return nil, nil, e.New(e.CodeEngineCapability, "job %q has an unsupported action parameter binding", jobID)
			}
			encoded, exists := entry.Values[part]
			if !exists {
				return nil, nil, e.New(e.CodePipelineDrift, "job %q omits action parameters %q", jobID, part)
			}
			var values map[string]string
			if err := canonical.UnmarshalExact([]byte(encoded), &values); err != nil || values == nil {
				return nil, nil, e.New(e.CodePipelineDrift, "job %q has invalid action parameters %q", jobID, part)
			}
			for key, value := range values {
				githubYAMLSubstitute(inner, githubParameterExpression(key), githubYAMLString(value))
			}
			if githubYAMLContains(inner, "${{ fromJSON(inputs.parameters)") {
				return nil, nil, e.New(e.CodePipelineDrift, "job %q has unresolved action parameters %q", jobID, part)
			}
			for stepIndex, step := range inner.Content {
				key := "s" + strconv.Itoa(stepIndex) + "_"
				if id, exists := values[key+"id"]; exists {
					if githubYAMLValue(step, "id") != "operation_"+strconv.Itoa(stepIndex) {
						return nil, nil, e.New(e.CodeEngineCapability, "shared action changes its operation ID")
					}
					githubYAMLSet(step, "id", githubYAMLString(id))
				}
				if selector, exists := values[key+"selector"]; exists {
					env := githubYAMLField(step, "env")
					run := githubYAMLField(step, "run")
					quotedVariable := `"$` + githubSelectorEnvironment + `"`
					if selector == "" || env == nil || env.Kind != yamlv3.MappingNode || run == nil || strings.Count(run.Value, quotedVariable) != 1 || githubYAMLValue(env, githubSelectorEnvironment) != selector {
						return nil, nil, e.New(e.CodeEngineCapability, "shared action changes its literal selector binding")
					}
					run.Value = strings.Replace(run.Value, quotedVariable, shellQuote(selector), 1)
					githubYAMLRemove(env, githubSelectorEnvironment)
					if len(env.Content) == 0 {
						githubYAMLRemove(step, "env")
					}
				}
			}
			githubYAMLRemove(inputs, "parameters")
			githubYAMLRemove(with, "parameters")
			if len(with.Content) == 0 {
				githubYAMLRemove(call, "with")
			}
			localPath := githubActionsDirectory + "jobs/" + jobID + "/" + part
			var err error
			materialized[localPath+"/action.yml"], err = encodeGitHubYAML(body)
			if err != nil {
				return nil, nil, err
			}
			githubYAMLSet(call, "uses", githubYAMLString("./"+localPath))
		}
	}
	workflow, err := encodeGitHubYAML(&document)
	return workflow, materialized, err
}

func githubActionPart(value string) bool {
	_, suffix, ok := strings.Cut(value, "-")
	if !ok || !(strings.HasPrefix(value, "part-") || strings.HasPrefix(value, "transfer-")) {
		return false
	}
	number, err := strconv.Atoi(suffix)
	return err == nil && number > 0 && strconv.Itoa(number) == suffix
}
