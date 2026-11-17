// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"path"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// This navigation index describes the emitted adapter, not Plan identity or
// execution authority. Commands come from the already sliced producer records.
type githubScriptIndex struct {
	Plan       string                     `json:"plan"`
	StoreIndex string                     `json:"storeIndex"`
	Jobs       map[string]githubScriptJob `json:"jobs"`
}

type githubScriptJob struct {
	Workflow   string                  `json:"workflow"`
	Operations []githubScriptOperation `json:"operations"`
}

type githubScriptOperation struct {
	Node          string          `json:"node"`
	Step          string          `json:"step"`
	Target        string          `json:"target,omitempty"`
	Producer      string          `json:"producer,omitempty"`
	Record        string          `json:"record,omitempty"`
	Command       json.RawMessage `json:"command,omitempty"`
	VendorCommand json.RawMessage `json:"vendorCmd,omitempty"`
}

func githubScriptIndexPath(plan *Plan) string {
	return path.Join(path.Dir(githubLayoutFor(plan).planPath), "script-index.json")
}

func addGitHubScriptIndex(plan *Plan, files map[string][]byte) error {
	layout := githubLayoutFor(plan)
	var root struct {
		Builds []struct {
			Name string `json:"name"`
			File string `json:"file"`
		} `json:"builds"`
	}
	if err := json.Unmarshal(files[layout.planPath], &root); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading producer index for script navigation")
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
			return e.Wrap(e.CodePipelineDrift, err, "reading producer commands for script navigation")
		}
		records[build.Name] = build.File
		producers[build.Name] = map[string]planFields{}
		for _, node := range record.Graph.Nodes {
			var id string
			if err := json.Unmarshal(node["id"], &id); err != nil {
				return e.Wrap(e.CodePipelineDrift, err, "reading producer identity for script navigation")
			}
			producers[build.Name][id] = node
		}
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return err
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
					return e.New(e.CodePipelineDrift, "script navigation cannot find producer %s/%s", node.BuildTarget, node.IRNode)
				}
				operation.Record = records[node.BuildTarget]
				operation.Command, operation.VendorCommand = producer["command"], producer["vendorCmd"]
			}
			job.Operations = append(job.Operations, operation)
		}
		jobs[stage.ID] = job
	}
	index := githubScriptIndex{Plan: layout.planPath, StoreIndex: PlanStoreIndexPath, Jobs: map[string]githubScriptJob{}}
	indexPath := githubScriptIndexPath(plan)
	for _, filename := range sortedKeys(files) {
		if !strings.HasPrefix(filename, ".github/workflows/") || !strings.HasSuffix(filename, ".yml") {
			continue
		}
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(files[filename], &document); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading workflow for script navigation")
		}
		if len(document.Content) != 1 {
			return e.New(e.CodePipelineDrift, "script navigation requires one workflow document")
		}
		mapping := githubYAMLField(document.Content[0], "jobs")
		if mapping == nil {
			return e.New(e.CodePipelineDrift, "script navigation requires workflow jobs")
		}
		comments := map[int]string{}
		raw := string(files[filename])
		for i := 0; i < len(mapping.Content); i += 2 {
			key := mapping.Content[i]
			job, ok := jobs[key.Value]
			if !ok {
				continue
			}
			if _, duplicate := index.Jobs[key.Value]; duplicate {
				return e.New(e.CodePipelineDrift, "duplicate script navigation job %s", key.Value)
			}
			job.Workflow = filename
			index.Jobs[key.Value] = job
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
	if len(index.Jobs) != len(jobs) {
		return e.New(e.CodePipelineDrift, "script navigation omits an execution job")
	}
	files[indexPath], err = treeJSON(index)
	return err
}
