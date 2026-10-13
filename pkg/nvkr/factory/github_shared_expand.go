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
			if err := materializeGitHubInterfaceSteps(inner, values, inputs, with); err != nil {
				return nil, nil, err
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

// Interpret only the declared optional slots and field-presence metadata of
// generated interfaces. This is not a provider-expression evaluator. The
// recovered steps undergo the existing graph checks and exact regeneration.
func materializeGitHubInterfaceSteps(steps *yamlv3.Node, values map[string]string, inputs, arguments *yamlv3.Node) error {
	var concrete []*yamlv3.Node
	for _, step := range steps.Content {
		id := githubYAMLValue(step, "id")
		role := strings.TrimPrefix(id, "operation_")
		if role == id || role == "" {
			return e.New(e.CodeEngineCapability, "shared action changes its named operation ID")
		}
		prefix := role + "_"
		present, exists := values[prefix+"present"]
		if !exists || (present != "true" && present != "false") {
			return e.New(e.CodePipelineDrift, "shared action omits presence for operation %q", role)
		}
		guard := "${{ fromJSON(inputs.parameters)." + prefix + "present == 'true' }}"
		condition := githubYAMLValue(step, "if")
		if present == "false" {
			if condition != guard {
				return e.New(e.CodeEngineCapability, "shared action disables required operation %q", role)
			}
			continue
		}
		if condition == guard {
			githubYAMLRemove(step, "if")
		} else if condition != "" && condition != githubAlwaysCondition {
			return e.New(e.CodeEngineCapability, "shared action changes operation %q condition", role)
		}
		if role == "upload" && values["upload_path_input"] != "" {
			path := githubYAMLField(githubYAMLField(step, "with"), "path")
			const expression = "${{ inputs.with-path || fromJSON(inputs.parameters).upload_with_path }}"
			if path == nil || path.Value != expression || githubYAMLField(inputs, "with-path") == nil || githubYAMLField(arguments, "with-path") == nil {
				return e.New(e.CodeEngineCapability, "shared upload changes its path interface")
			}
			switch values["upload_path_input"] {
			case "true":
				*path = *githubYAMLString("${{ inputs.with-path }}")
			case "false":
				if githubYAMLValue(arguments, "with-path") != "" {
					return e.New(e.CodeEngineCapability, "shared upload overrides its Plan path")
				}
				*path = *githubYAMLString(githubParameterExpression("upload_with_path"))
				githubYAMLRemove(inputs, "with-path")
				githubYAMLRemove(arguments, "with-path")
			default:
				return e.New(e.CodePipelineDrift, "shared upload has invalid path-origin metadata")
			}
		}
		for key, value := range values {
			githubYAMLSubstitute(step, githubParameterExpression(key), githubYAMLString(value))
		}
		if githubYAMLContains(step, "${{ fromJSON(inputs.parameters)") {
			return e.New(e.CodePipelineDrift, "shared operation %q has unresolved parameters", role)
		}
		originalID, exists := values[prefix+"id"]
		if !exists {
			return e.New(e.CodePipelineDrift, "shared operation %q omits ID metadata", role)
		}
		if originalID == "" {
			githubYAMLRemove(step, "id")
		} else {
			githubYAMLSet(step, "id", githubYAMLString(originalID))
		}
		if with := githubYAMLField(step, "with"); with != nil {
			for index := len(with.Content) - 2; index >= 0; index -= 2 {
				key := with.Content[index].Value
				presence := prefix + "with_" + strings.ReplaceAll(key, "-", "_") + "_present"
				if defined, exists := values[presence]; exists {
					switch defined {
					case "true":
					case "false":
						githubYAMLRemove(with, key)
					default:
						return e.New(e.CodePipelineDrift, "shared operation %q has invalid input-presence metadata", role)
					}
				}
			}
		}
		if selector, exists := values[prefix+"selector"]; exists {
			env := githubYAMLField(step, "env")
			run := githubYAMLField(step, "run")
			quotedVariable := `"$` + githubSelectorEnvironment + `"`
			if selector == "" || env == nil || env.Kind != yamlv3.MappingNode || run == nil || strings.Count(run.Value, quotedVariable) != 1 || githubYAMLValue(env, githubSelectorEnvironment) != selector {
				return e.New(e.CodeEngineCapability, "shared operation %q changes its selector binding", role)
			}
			run.Value = strings.Replace(run.Value, quotedVariable, shellQuote(selector), 1)
			githubYAMLRemove(env, githubSelectorEnvironment)
			if len(env.Content) == 0 {
				githubYAMLRemove(step, "env")
			}
		}
		if run := githubYAMLField(step, "run"); run != nil {
			switch values[prefix+"run_newline"] {
			case "true":
				run.Value = strings.TrimSuffix(run.Value, "\n") + "\n"
			case "false":
				run.Value = strings.TrimSuffix(run.Value, "\n")
			default:
				return e.New(e.CodePipelineDrift, "shared operation %q omits run formatting metadata", role)
			}
		}
		concrete = append(concrete, step)
	}
	steps.Content = concrete
	return nil
}

func githubActionPart(value string) bool {
	_, suffix, ok := strings.Cut(value, "-")
	if !ok || !(strings.HasPrefix(value, "part-") || strings.HasPrefix(value, "transfer-")) {
		return false
	}
	number, err := strconv.Atoi(suffix)
	return err == nil && number > 0 && strconv.Itoa(number) == suffix
}
