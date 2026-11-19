// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// Navigation describes emitted jobs, never Plan identity or execution authority.
type githubScriptJob struct {
	Workflow   string                  `json:"workflow"`
	Operations []githubScriptOperation `json:"operations"`
}

type githubScriptOperation struct {
	Node     string   `json:"node"`
	Step     string   `json:"step"`
	Target   string   `json:"target,omitempty"`
	Producer string   `json:"producer,omitempty"`
	Record   string   `json:"record,omitempty"`
	Scripts  []string `json:"scripts,omitempty"`
}

func addGitHubScriptIndex(plan *Plan, files map[string][]byte) error {
	_, err := githubScriptNavigation(plan, files)
	return err
}

// Comments point to the one store-wide index. The returned job records are
// grouped across bundles when generation computes the descriptive index.
func githubScriptNavigation(plan *Plan, files map[string][]byte) (map[string]githubScriptJob, error) {
	layout := githubLayoutFor(plan)
	var root struct {
		Builds []struct {
			Name string `json:"name"`
			File string `json:"file"`
		} `json:"builds"`
	}
	if err := json.Unmarshal(files[layout.planPath], &root); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading producer index for script navigation")
	}
	records := map[string]string{}
	producers := map[string]map[string]planFields{}
	for _, build := range root.Builds {
		var record struct {
			Graph struct {
				Nodes []planFields `json:"nodes"`
			} `json:"executionGraph"`
		}
		if err := json.Unmarshal(files[build.File], &record); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading producer commands for script navigation")
		}
		records[build.Name] = build.File
		producers[build.Name] = map[string]planFields{}
		for _, node := range record.Graph.Nodes {
			var id string
			if err := json.Unmarshal(node["id"], &id); err != nil {
				return nil, e.Wrap(e.CodePipelineDrift, err, "reading producer identity for script navigation")
			}
			producers[build.Name][id] = node
		}
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, err
	}
	jobs := map[string]githubScriptJob{}
	for _, stage := range stages {
		job := githubScriptJob{Operations: []githubScriptOperation{}}
		for _, node := range stage.Nodes {
			step := githubJobID(node.ID)
			if stage.DiskOutput != "" {
				step = "run-disk"
			}
			operation := githubScriptOperation{Node: node.ID, Step: step, Target: node.BuildTarget, Producer: node.IRNode}
			if node.IRNode != "" {
				producer := producers[node.BuildTarget][node.IRNode]
				if producer == nil {
					return nil, e.New(e.CodePipelineDrift, "script navigation cannot find producer %s/%s", node.BuildTarget, node.IRNode)
				}
				operation.Record = records[node.BuildTarget]
				operation.Scripts, err = githubCommandScripts(producer["command"], producer["vendorCmd"])
				if err != nil {
					return nil, err
				}
			}
			job.Operations = append(job.Operations, operation)
		}
		jobs[stage.ID] = job
	}
	index := map[string]githubScriptJob{}
	indexPath := PlanStoreIndexPath
	for _, filename := range sortedKeys(files) {
		if !strings.HasPrefix(filename, ".github/workflows/") || !strings.HasSuffix(filename, ".yml") {
			continue
		}
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(files[filename], &document); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading workflow for script navigation")
		}
		if len(document.Content) != 1 {
			return nil, e.New(e.CodePipelineDrift, "script navigation requires one workflow document")
		}
		mapping := githubYAMLField(document.Content[0], "jobs")
		if mapping == nil {
			return nil, e.New(e.CodePipelineDrift, "script navigation requires workflow jobs")
		}
		comments := map[int]string{}
		raw := string(files[filename])
		for i := 0; i < len(mapping.Content); i += 2 {
			key := mapping.Content[i]
			job, ok := jobs[key.Value]
			if !ok {
				continue
			}
			if _, duplicate := index[key.Value]; duplicate {
				return nil, e.New(e.CodePipelineDrift, "duplicate script navigation job %s", key.Value)
			}
			job.Workflow = filename
			index[key.Value] = job
			comment := "    # Producer commands: " + indexPath + " (job " + key.Value + ")"
			if !strings.Contains(raw, comment+"\n") {
				comments[key.Line] = comment
			}
		}
		var out strings.Builder
		for i, line := range strings.SplitAfter(raw, "\n") {
			out.WriteString(line)
			if comment, ok := comments[i+1]; ok {
				out.WriteString(comment + "\n")
			}
		}
		files[filename] = []byte(out.String())
	}
	if len(index) != len(jobs) {
		return nil, e.New(e.CodePipelineDrift, "script navigation omits an execution job")
	}
	return index, nil
}

func githubCommandScripts(commands ...json.RawMessage) ([]string, error) {
	var scripts []string
	for _, command := range commands {
		if len(command) == 0 {
			continue
		}
		var argv []json.RawMessage
		if err := json.Unmarshal(command, &argv); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading navigation command")
		}
		for _, operand := range argv {
			var literal string
			if json.Unmarshal(operand, &literal) == nil {
				continue
			}
			var ref map[string]string
			if json.Unmarshal(operand, &ref) != nil || len(ref) != 1 || !isPlanStoreScript(ref["file"]) {
				return nil, e.New(e.CodePipelineDrift, "invalid navigation script reference")
			}
			scripts = append(scripts, ref["file"])
		}
	}
	slices.Sort(scripts)
	return slices.Compact(scripts), nil
}
