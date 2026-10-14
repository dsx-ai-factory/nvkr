// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const GitHubActionDataPath = ".github/nvkr-run/action-data.json"

func githubActionDataPathFor(plan *Plan) string {
	return strings.TrimSuffix(githubLayoutFor(plan).planPath, "plan.json") + "action-data.json"
}

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

// shareGitHubActions projects generated operations onto named action interfaces.
// The Plan remains execution authority. The descriptor contains only literal
// labels, selectors and transport settings; no run programs or credentials.
func shareGitHubActions(plan *Plan, raw []byte, files map[string][]byte, actions githubActionRefs) ([]byte, error) {
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
	for index := 0; index < len(jobs.Content); index += 2 {
		jobID, job := jobs.Content[index].Value, jobs.Content[index+1]
		steps := githubYAMLField(job, "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode {
			return nil, e.New(e.CodePipelineDrift, "shared action job %q requires steps", jobID)
		}
		entry := githubActionDataEntry{Template: "stage-context", Values: map[string]string{}}
		var rewritten []*yamlv3.Node
		prepared := false
		part, transfer := 0, 0
		for _, call := range steps.Content {
			if githubYAMLValue(call, "id") == githubContextStepID {
				return nil, e.New(e.CodeEngineCapability, "job %q uses reserved action context ID", jobID)
			}
			uses := githubYAMLValue(call, "uses")
			prefix := "./" + githubActionsDirectoryFor(plan) + "jobs/" + jobID + "/"
			if !strings.HasPrefix(uses, prefix) {
				rewritten = append(rewritten, call)
				continue
			}
			path := strings.TrimPrefix(uses, "./") + "/action.yml"
			if strings.HasPrefix(jobID, "build-runner-") {
				if err := shareGitHubBootstrap(plan, files, path, call); err != nil {
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
			if len(inner.Content) > 0 && strings.HasPrefix(githubYAMLValue(inner.Content[0], "uses"), "./"+githubActionsDirectoryFor(plan)+"prepare-runner") {
				if prepared {
					return nil, e.New(e.CodeEngineCapability, "job %q prepares its helper twice", jobID)
				}
				prepare, err := githubPrepareStage(plan, files, inner.Content[0])
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
			invocations, err := projectGitHubActionInterfaces(plan, body, call, actions)
			if err != nil {
				return nil, err
			}
			controlled := false
			for _, key := range githubTransferControlFields {
				controlled = controlled || githubYAMLField(call, key) != nil
			}
			if controlled && len(invocations) != 1 {
				return nil, e.New(e.CodeEngineCapability, "controlled action in job %q must project to one transport", jobID)
			}
			for _, invocation := range invocations {
				part++
				key := fmt.Sprintf("part-%d", part)
				if controlled {
					part--
					transfer++
					key = fmt.Sprintf("transfer-%d", transfer)
				}
				parameters, err := canonical.Marshal(invocation.Values)
				if err != nil {
					return nil, err
				}
				entry.Values[key] = string(parameters)
				path := githubActionsDirectoryFor(plan) + "shared/" + invocation.Family
				templateRaw, err := encodeGitHubYAML(invocation.Body)
				if err != nil {
					return nil, err
				}
				if previous, exists := files[path+"/action.yml"]; exists && !bytes.Equal(previous, templateRaw) {
					return nil, e.New(e.CodeEngineCapability, "action interface %q has conflicting implementations", invocation.Family)
				}
				files[path+"/action.yml"] = templateRaw
				with := invocation.Arguments
				if with == nil {
					with = githubYAMLMap()
				}
				githubYAMLSet(with, "parameters", githubYAMLString(githubParametersExpression(key)))
				// The job and inner role labels retain the selected producer's name.
				// A stable interface label lets identical complete calls share a YAML
				// anchor without naming a different producer in a sibling job.
				label := githubYAMLValue(invocation.Body, "name")
				if controlled {
					label = invocation.Name
				}
				projected := githubYAMLMap("name", githubYAMLString(label), "uses", githubYAMLString("./"+path), "with", with)
				for _, field := range githubTransferControlFields {
					if value := githubYAMLField(call, field); value != nil {
						githubYAMLSet(projected, field, value)
					}
				}
				rewritten = append(rewritten, projected)
			}
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
	var err error
	files[githubActionDataPathFor(plan)], err = canonical.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err := anchorGitHubPreflight(plan, jobs, actions); err != nil {
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

func githubPrepareStage(plan *Plan, files map[string][]byte, helper *yamlv3.Node) (*yamlv3.Node, error) {
	container := strings.HasSuffix(githubYAMLValue(helper, "uses"), "-container")
	path, shell := githubActionsDirectoryFor(plan)+"shared/prepare-stage", "bash -e {0}"
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
				"run", githubYAMLString(githubRunnerCommand(plan, "describe-action", "", "")+` --action "$NVKR_ACTION"`)),
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
