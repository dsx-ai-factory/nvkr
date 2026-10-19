// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// GitHub rejects workflow files above 500 KB.
const maxGitHubWorkflowBytes = 500000

// GitHub's anchor-aware YAML reader counts every parsing event, including
// repeated aliases and container start/end events, and rejects more than 50,000:
// https://github.com/actions/runner/blob/796cc18ffaaafd5cea525d6231f5a8c93c11c603/src/Sdk/WorkflowParser/Conversion/YamlObjectReader.cs#L420
// This is an implementation limit rather than a documented permanent service
// contract. The maintained fixtures separately reserve five percent headroom.
const maxGitHubWorkflowYAMLNodes = 50000

const maxGitHubWorkflowYAMLDepth = 100

func validateGitHubWorkflowSize(raw []byte) error {
	if len(raw) > maxGitHubWorkflowBytes {
		return e.New(e.CodeEngineCapability, "generated workflow is %d bytes; GitHub permits at most %d bytes per file", len(raw), maxGitHubWorkflowBytes)
	}
	_, err := countGitHubWorkflowYAMLNodes(raw)
	return err
}

// countGitHubWorkflowYAMLNodes counts the provider's parsing events without
// materializing expanded aliases. Its bounded traversal stops before an alias
// graph can consume unbounded memory, stack or CPU.
func countGitHubWorkflowYAMLNodes(raw []byte) (int, error) {
	decoder := yamlv3.NewDecoder(bytes.NewReader(raw))
	var document yamlv3.Node
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return 0, e.New(e.CodeInvalidCatalog, "generated workflow is empty")
		}
		return 0, e.Wrap(e.CodePipelineDrift, err, "reading generated workflow YAML for provider admission")
	}
	var trailing yamlv3.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return 0, e.New(e.CodePipelineDrift, "generated workflow must contain exactly one YAML document")
	}

	// A yaml.Node document omits the stream's start and end events.
	nodes := 2
	active := make(map[*yamlv3.Node]bool)
	var visit func(*yamlv3.Node, int) error
	visit = func(node *yamlv3.Node, depth int) error {
		if node == nil || active[node] {
			return e.New(e.CodePipelineDrift, "generated workflow contains a missing or recursive YAML alias")
		}
		if depth > maxGitHubWorkflowYAMLDepth {
			return e.New(e.CodeEngineCapability, "generated workflow exceeds the local YAML nesting limit of %d", maxGitHubWorkflowYAMLDepth)
		}
		active[node] = true
		defer delete(active, node)
		if node.Kind == yamlv3.AliasNode {
			return visit(node.Alias, depth+1)
		}

		cost := 0
		switch node.Kind {
		case yamlv3.ScalarNode:
			cost = 1
		case yamlv3.DocumentNode, yamlv3.MappingNode, yamlv3.SequenceNode:
			cost = 2
		default:
			return e.New(e.CodePipelineDrift, "generated workflow contains an unsupported YAML node")
		}
		if cost > maxGitHubWorkflowYAMLNodes-nodes {
			return e.New(e.CodeEngineCapability, "generated workflow exceeds GitHub's parser implementation limit of %d YAML parsing events after alias expansion", maxGitHubWorkflowYAMLNodes)
		}
		nodes += cost
		for _, child := range node.Content {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := visit(&document, 1)
	return nodes, err
}
