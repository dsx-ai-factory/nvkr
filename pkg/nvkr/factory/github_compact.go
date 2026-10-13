// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

// GitHub rejects workflow files above 500 KB. Anchors are a presentation-only
// compression: the expanded job graph and permission boundaries remain exact.
const maxGitHubWorkflowBytes = 500000

func compactGitHubWorkflow(raw []byte) ([]byte, error) {
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "parse generated workflow for compaction")
	}
	keys := map[*yamlv3.Node]string{}
	counts := map[string]int{}
	var scan func(*yamlv3.Node) error
	scan = func(node *yamlv3.Node) error {
		encoded, err := yamlv3.Marshal(node)
		if err != nil {
			return err
		}
		if node.Kind != yamlv3.DocumentNode && len(encoded) >= 24 {
			key := string(encoded)
			keys[node] = key
			counts[key]++
		}
		for i, child := range node.Content {
			if node.Kind == yamlv3.MappingNode && i%2 == 0 {
				continue
			}
			if err := scan(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := scan(&document); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "index repeated workflow values")
	}
	anchors := map[string]*yamlv3.Node{}
	var rewrite func(*yamlv3.Node)
	rewrite = func(node *yamlv3.Node) {
		key := keys[node]
		if key != "" && counts[key] > 1 {
			if first := anchors[key]; first != nil {
				*node = yamlv3.Node{Kind: yamlv3.AliasNode, Value: first.Anchor, Alias: first}
				return
			}
			node.Anchor = fmt.Sprintf("n%d", len(anchors)+1)
			anchors[key] = node
		}
		for i, child := range node.Content {
			if node.Kind == yamlv3.MappingNode && i%2 == 0 {
				continue
			}
			rewrite(child)
		}
	}
	rewrite(&document)
	// Aliasing a whole step can make its nested anchors unnecessary. Retain
	// only definitions referenced by the final document (actionlint requires it).
	used := map[*yamlv3.Node]bool{}
	var references func(*yamlv3.Node)
	references = func(node *yamlv3.Node) {
		if node.Kind == yamlv3.AliasNode {
			used[node.Alias] = true
		}
		for _, child := range node.Content {
			references(child)
		}
	}
	references(&document)
	for _, node := range anchors {
		if !used[node] {
			node.Anchor = ""
		}
	}
	// Keep jobs in block form; inline small step/permission maps. This removes
	// repeated indentation without changing expressions, strings or run scripts.
	var inline func(*yamlv3.Node, int)
	inline = func(node *yamlv3.Node, depth int) {
		if depth >= 4 && node.Kind == yamlv3.MappingNode {
			node.Style |= yamlv3.FlowStyle
		}
		for _, child := range node.Content {
			inline(child, depth+1)
		}
	}
	inline(&document, 0)
	var output bytes.Buffer
	encoder := yamlv3.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "encode compact workflow")
	}
	if err := encoder.Close(); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "close compact workflow")
	}
	before, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decode original workflow")
	}
	after, err := yaml.YAMLToJSONStrict(output.Bytes())
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "expand workflow aliases")
	}
	if !bytes.Equal(before, after) {
		return nil, e.New(e.CodePipelineDrift, "workflow compaction changed the executable graph")
	}
	return output.Bytes(), nil
}

func validateGitHubWorkflowSize(raw []byte) error {
	if len(raw) > maxGitHubWorkflowBytes {
		return e.New(e.CodeEngineCapability, "generated workflow is %d bytes; GitHub permits at most %d bytes per file", len(raw), maxGitHubWorkflowBytes)
	}
	return nil
}
