// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const GitHubActionDataPath = ".github/nvkr-run/action-data.json"

const githubSharedDirectory = githubActionsDirectory + "shared/"
const githubContextStepID = "nvkr-context"
const githubSelectorEnvironment = "NVKR_ACTION_SELECTOR"

type githubActionDataDocument struct {
	Entries map[string]githubActionDataEntry `json:"entries"`
}

type githubActionDataEntry struct {
	Template string            `json:"template"`
	Values   map[string]string `json:"values"`
}

type githubSharedTemplate struct {
	family string
	key    string
	raw    []byte
	calls  []*yamlv3.Node
}

// shareGitHubActions factors closed, generated step sequences into templates.
// The Plan remains execution authority. The descriptor contains only literal
// labels, selectors and transport settings; no run programs or credentials.
func shareGitHubActions(raw []byte, files map[string][]byte, actions githubActionRefs) ([]byte, error) {
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "parse workflow for shared actions")
	}
	if len(document.Content) != 1 {
		return nil, e.New(e.CodePipelineDrift, "shared actions require a workflow document")
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode {
		return nil, e.New(e.CodePipelineDrift, "shared actions require jobs")
	}
	data := githubActionDataDocument{Entries: map[string]githubActionDataEntry{}}
	templates := map[string]*githubSharedTemplate{}
	for index := 0; index < len(jobs.Content); index += 2 {
		jobID, job := jobs.Content[index].Value, jobs.Content[index+1]
		steps := githubYAMLField(job, "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode {
			return nil, e.New(e.CodePipelineDrift, "shared action job %q requires steps", jobID)
		}
		entry := githubActionDataEntry{Template: "stage-context", Values: map[string]string{}}
		var rewritten []*yamlv3.Node
		prepared := false
		for _, call := range steps.Content {
			if githubYAMLValue(call, "id") == githubContextStepID {
				return nil, e.New(e.CodeEngineCapability, "job %q uses reserved action context ID", jobID)
			}
			uses := githubYAMLValue(call, "uses")
			prefix := "./" + githubActionsDirectory + "jobs/" + jobID + "/"
			if !strings.HasPrefix(uses, prefix) {
				rewritten = append(rewritten, call)
				continue
			}
			path := strings.TrimPrefix(uses, "./") + "/action.yml"
			if strings.HasPrefix(jobID, "build-runner-") {
				if err := shareGitHubBootstrap(files, path, call); err != nil {
					return nil, err
				}
				rewritten = append(rewritten, call)
				continue
			}
			var action yamlv3.Node
			if err := yamlv3.Unmarshal(files[path], &action); err != nil || len(action.Content) != 1 {
				return nil, e.New(e.CodePipelineDrift, "invalid generated action %q", path)
			}
			body := action.Content[0]
			inner := githubYAMLField(githubYAMLField(body, "runs"), "steps")
			if inner == nil || inner.Kind != yamlv3.SequenceNode {
				return nil, e.New(e.CodePipelineDrift, "generated action %q requires steps", path)
			}
			if len(inner.Content) > 0 && strings.HasPrefix(githubYAMLValue(inner.Content[0], "uses"), "./"+githubActionsDirectory+"prepare-runner") {
				if prepared {
					return nil, e.New(e.CodeEngineCapability, "job %q prepares its helper twice", jobID)
				}
				prepare, err := githubPrepareStage(files, inner.Content[0])
				if err != nil {
					return nil, err
				}
				rewritten = append(rewritten, prepare)
				inner.Content = inner.Content[1:]
				prepared = true
			}
			if !prepared {
				return nil, e.New(e.CodeEngineCapability, "job %q uses action data before helper preparation", jobID)
			}
			delete(files, path)
			if len(inner.Content) == 0 {
				continue
			}
			part := strings.TrimPrefix(uses, prefix)
			values, family, err := githubParameterizeAction(body, inner, actions)
			if err != nil {
				return nil, err
			}
			parameters, err := canonical.Marshal(values)
			if err != nil {
				return nil, err
			}
			entry.Values[part] = string(parameters)
			with := githubYAMLField(call, "with")
			if with == nil {
				with = githubYAMLMap()
				githubYAMLSet(call, "with", with)
			}
			githubYAMLSet(with, "parameters", githubYAMLString(githubParametersExpression(part)))
			templateRaw, err := encodeGitHubYAML(body)
			if err != nil {
				return nil, err
			}
			// Pins are literal in each template, but never part of its grouping
			// key. Updating a dependency cannot rename the caller's template.
			key := string(templateRaw)
			for _, ref := range []string{actions.Checkout, actions.SetupGo, actions.Upload, actions.Download} {
				if repository, _, ok := strings.Cut(ref, "@"); ok {
					key = strings.ReplaceAll(key, ref, repository+"@PIN")
				}
			}
			if prior := templates[key]; prior != nil {
				prior.calls = append(prior.calls, call)
			} else {
				templates[key] = &githubSharedTemplate{family: family, key: key, raw: templateRaw, calls: []*yamlv3.Node{call}}
			}
			rewritten = append(rewritten, call)
		}
		steps.Content = rewritten
		if prepared {
			encoded, err := canonical.Marshal(entry.Values)
			if err != nil {
				return nil, err
			}
			if len(encoded)+len("data=\n") > maxGitHubActionDataOutputBytes {
				return nil, e.New(e.CodeEngineCapability, "job %q shared action data exceeds the provider output bound", jobID)
			}
			data.Entries[jobID] = entry
		}
	}
	ordered := make([]*githubSharedTemplate, 0, len(templates))
	for _, template := range templates {
		ordered = append(ordered, template)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].family == ordered[j].family {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].family < ordered[j].family
	})
	variants := map[string]int{}
	for _, template := range ordered {
		variants[template.family]++
		name := template.family
		if variants[name] > 1 {
			name += "-variant-" + strconv.Itoa(variants[name])
		}
		path := githubSharedDirectory + name
		files[path+"/action.yml"] = template.raw
		for _, call := range template.calls {
			githubYAMLSet(call, "uses", githubYAMLString("./"+path))
		}
	}
	var err error
	files[GitHubActionDataPath], err = canonical.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err := anchorGitHubPreflight(jobs, actions); err != nil {
		return nil, err
	}
	return encodeGitHubYAML(&document)
}

func githubParametersExpression(part string) string {
	return "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['" + part + "'] }}"
}

func githubParameterExpression(key string) string {
	return "${{ fromJSON(inputs.parameters)." + key + " }}"
}

func githubParameterizeAction(action, steps *yamlv3.Node, actions githubActionRefs) (map[string]string, string, error) {
	values := map[string]string{}
	inputs := githubYAMLField(action, "inputs")
	if inputs == nil {
		inputs = githubYAMLMap()
		githubYAMLSet(action, "inputs", inputs)
	}
	githubYAMLSet(inputs, "parameters", githubActionInput("Plan-derived parameters for this operation; never shell source"))
	nodes, uploads, downloads, packs := 0, 0, 0, 0
	source, restore, container, finalize := false, false, false, false
	for index, step := range steps.Content {
		prefix := fmt.Sprintf("s%d_", index)
		parameter := func(key string, value *yamlv3.Node) {
			values[prefix+key] = value.Value
			*value = *githubYAMLString(githubParameterExpression(prefix + key))
		}
		if name := githubYAMLField(step, "name"); name != nil {
			parameter("name", name)
		}
		if id := githubYAMLField(step, "id"); id != nil {
			values[prefix+"id"] = id.Value
			id.Value = "operation_" + strconv.Itoa(index)
		}
		if with := githubYAMLField(step, "with"); with != nil {
			for pos := 0; pos < len(with.Content); pos += 2 {
				value := with.Content[pos+1]
				if !strings.Contains(value.Value, "${{") {
					parameter("with_"+strings.ReplaceAll(with.Content[pos].Value, "-", "_"), value)
				}
			}
		}
		env := githubYAMLField(step, "env")
		if env != nil {
			for pos := 0; pos < len(env.Content); pos += 2 {
				key, value := env.Content[pos].Value, env.Content[pos+1]
				source = source || key == sourceTokenEnv
				// Credential-helper programs and provider/input expressions stay
				// literal in the template, never in descriptive output data.
				if !strings.Contains(value.Value, "${{") && !strings.HasPrefix(value.Value, "!") {
					parameter("env_"+key, value)
				}
			}
		}
		uses := githubYAMLValue(step, "uses")
		if uses == actions.Upload {
			uploads++
		}
		if uses == actions.Download {
			downloads++
		}
		run := githubYAMLField(step, "run")
		if run == nil {
			continue
		}
		container = container || githubYAMLValue(step, "shell") == "sh -e {0}"
		finalize = finalize || strings.Contains(run.Value, "./nvkr-exec run finalize ") || strings.Contains(run.Value, "factory github recover ")
		for _, operation := range []struct{ name, flag string }{{"run-node", "node"}, {"pack-outputs", "node"}, {"restore-inputs", "stage"}, {"prepare-images", "stage"}} {
			command := githubRunnerCommand(operation.name, "", "") + " --" + operation.flag + " "
			if !strings.HasPrefix(run.Value, command) {
				continue
			}
			quoted := strings.TrimSuffix(strings.TrimPrefix(run.Value, command), "\n")
			selector := quoted
			if strings.HasPrefix(selector, "'") && strings.HasSuffix(selector, "'") {
				selector = strings.ReplaceAll(selector[1:len(selector)-1], `'"'"'`, "'")
			}
			if shellQuote(selector) != quoted {
				return nil, "", e.New(e.CodeEngineCapability, "shared action requires one literal %s selector", operation.flag)
			}
			values[prefix+"selector"] = selector
			if env == nil {
				env = githubYAMLMap()
				githubYAMLSet(step, "env", env)
			}
			if githubYAMLField(env, githubSelectorEnvironment) != nil {
				return nil, "", e.New(e.CodeEngineCapability, "shared action selector environment is already defined")
			}
			githubYAMLSet(env, githubSelectorEnvironment, githubYAMLString(githubParameterExpression(prefix+"selector")))
			run.Value = strings.Replace(run.Value, command+quoted, command+`"$`+githubSelectorEnvironment+`"`, 1)
			switch operation.name {
			case "run-node":
				nodes++
			case "pack-outputs":
				packs++
			case "restore-inputs":
				restore = true
			}
			break
		}
	}
	family := "prepare-stage-inputs"
	switch {
	case finalize:
		family = "finalize-run"
	case nodes == 1:
		family = fmt.Sprintf("execute-node-%d-carriers", uploads)
	case nodes > 1:
		family = fmt.Sprintf("execute-group-%d-nodes-%d-carriers", nodes, uploads)
	case packs > 0:
		family = fmt.Sprintf("collect-%d-carriers", uploads)
	case uploads > 0:
		family = "upload-carrier"
	case downloads > 0:
		family = "download-carriers"
	}
	if source {
		family += "-source"
	}
	if restore {
		family += "-with-inputs"
	}
	if container {
		family += "-container"
	}
	githubYAMLSet(action, "name", githubYAMLString(strings.ReplaceAll(family, "-", " ")))
	// YAML decoding attaches the old generated marker to the first key.
	// Replace it together with the document header instead of duplicating it.
	action.Content[0].HeadComment = ""
	action.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	return values, family, nil
}

func githubPrepareStage(files map[string][]byte, helper *yamlv3.Node) (*yamlv3.Node, error) {
	container := strings.HasSuffix(githubYAMLValue(helper, "uses"), "-container")
	path, shell := githubSharedDirectory+"prepare-stage", "bash -e {0}"
	if container {
		path, shell = path+"-container", "sh -e {0}"
	}
	artifact := githubYAMLField(githubYAMLField(helper, "with"), "runner-artifact")
	if artifact == nil {
		return nil, e.New(e.CodePipelineDrift, "shared stage preparation requires a helper artifact")
	}
	value := *artifact
	*artifact = *githubYAMLString("${{ inputs.runner-artifact }}")
	action := githubYAMLMap("name", githubYAMLString("Prepare NVKR runner and job parameters"),
		"description", githubYAMLString("Prepare the helper and derive bounded data from the admitted Plan before producer execution."),
		"inputs", githubYAMLMap("runner-artifact", githubActionInput("Same-Run helper artifact"), "action", githubActionInput("Stable job selector")),
		"outputs", githubYAMLMap("data", githubYAMLMap("description", githubYAMLString("Plan-derived operation parameters"), "value", githubYAMLString("${{ steps.describe.outputs.data }}"))),
		"runs", githubYAMLMap("using", githubYAMLString("composite"), "steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: []*yamlv3.Node{
			helper,
			githubYAMLMap("name", githubYAMLString("Resolve job parameters from the admitted Plan"), "id", githubYAMLString("describe"),
				"env", githubYAMLMap("NVKR_ACTION", githubYAMLString("${{ inputs.action }}")), "shell", githubYAMLString(shell),
				"run", githubYAMLString(githubRunnerCommand("describe-action", "", "")+` --action "$NVKR_ACTION"`)),
		}}))
	action.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	raw, err := encodeGitHubYAML(action)
	if err != nil {
		return nil, err
	}
	if prior, ok := files[path+"/action.yml"]; ok && !bytes.Equal(prior, raw) {
		return nil, e.New(e.CodeEngineCapability, "shared stage preparation has conflicting definitions")
	}
	files[path+"/action.yml"] = raw
	return githubYAMLMap("name", githubYAMLString("Prepare NVKR runner and job parameters"), "id", githubYAMLString(githubContextStepID),
		"uses", githubYAMLString("./"+path), "with", githubYAMLMap("runner-artifact", &value, "action", githubYAMLString("${{ github.job }}"))), nil
}
