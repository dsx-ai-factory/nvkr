// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"strconv"
	"strings"

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
	before, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decode original workflow")
	}
	// Callers may recompact an edited generated workflow. Resolve its aliases
	// before assigning new anchors so old names cannot bind to new definitions.
	var expand func(*yamlv3.Node) yamlv3.Node
	expand = func(node *yamlv3.Node) yamlv3.Node {
		if node.Kind == yamlv3.AliasNode {
			return expand(node.Alias)
		}
		result := *node
		result.Anchor = ""
		result.Content = make([]*yamlv3.Node, len(node.Content))
		for i, child := range node.Content {
			value := expand(child)
			result.Content[i] = &value
		}
		return result
	}
	// The strict decode above rejects recursive and excessively expanding aliases.
	document = expand(&document)
	keys := map[*yamlv3.Node]string{}
	counts := map[string]int{}
	var scan func(*yamlv3.Node) error
	scan = func(node *yamlv3.Node) error {
		encoded, err := yamlv3.Marshal(node)
		if err != nil {
			return err
		}
		// Short repeated runner labels and read-only permission maps also pay for
		// their anchors in the full disk workflow, including explicit retention.
		if node.Kind != yamlv3.DocumentNode && len(encoded) >= 12 {
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
			// Anchor identifiers are document-local presentation, not authority or
			// user-facing labels. Unprefixed base-36 identifiers save thousands of
			// repeated bytes in full-catalog workflows without changing their graph.
			node.Anchor = strconv.FormatInt(int64(len(anchors)+1), 36)
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
	// Keep the workflow and job names on separate lines, but inline each job's
	// value. Whole-job flow maps remove repeated indentation from large material
	// closures without changing expressions, strings, names or run scripts.
	var inline func(*yamlv3.Node, int)
	inline = func(node *yamlv3.Node, depth int) {
		if node.Kind == yamlv3.ScalarNode {
			// The encoder selects ordinary quoting from the preserved YAML tag,
			// but YAML 1.1 consumers also treat these YAML 1.2 strings as booleans.
			node.Style = 0
			if node.Tag == "!!str" {
				switch strings.ToLower(node.Value) {
				case "y", "yes", "n", "no", "on", "off":
					node.Style = yamlv3.SingleQuotedStyle
				}
			}
		}
		if depth >= 3 && (node.Kind == yamlv3.MappingNode || node.Kind == yamlv3.SequenceNode) {
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
