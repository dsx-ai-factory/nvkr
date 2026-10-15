// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func (graph ArtifactGraph) Validate() error {
	if graph.SchemaVersion != ArtifactGraphSchema || len(graph.Nodes) == 0 || len(graph.Artifacts) == 0 {
		return e.New(e.CodeInvalidCatalog, "artifact graph schema or contents are invalid")
	}
	nodes := make(map[string]Node, len(graph.Nodes))
	for index, node := range graph.Nodes {
		if !safeGraphID(node.ID) || nodes[node.ID].ID != "" {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q is invalid or repeated", node.ID)
		}
		if node.ImageIndex != nil {
			if node.Kind != NodePackage || node.PackageKind != PackageImage || !strings.HasPrefix(node.ID, "package/") {
				return e.New(e.CodeInvalidCatalog, "image index requirements on non-image package operation")
			}
			if err := node.ImageIndex.ValidateFor(strings.TrimPrefix(node.ID, "package/"), "", nil); err != nil {
				return err
			}
		}
		producer := node.IRNode != ""
		if producer {
			if node.BuildTarget == "" || (node.Kind != NodeFetch && node.Kind != NodeBuild && node.Kind != NodeAcquire) {
				return e.New(e.CodePipelineDrift, "operation %q has an invalid producer reference", node.ID)
			}
			if node.RunsOn != "" || node.Network != "" || node.Container != "" || node.Platform != "" || node.Requirements != nil || node.ExecutionBinding != nil {
				return e.New(e.CodePipelineDrift, "operation %q duplicates producer execution settings", node.ID)
			}
		}
		if !producer {
			if node.Kind == NodeBuild || node.Kind == NodeFetch {
				return e.New(e.CodeEngineCapability, "operation %q lacks an exact producer reference", node.ID)
			}
			if strings.TrimSpace(node.RunsOn) == "" || strings.ContainsAny(node.RunsOn, "\r\n\x00") {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q has no safe runner label", node.ID)
			}
			if node.Container != "" && !containerPin.MatchString(node.Container) {
				return e.New(e.CodeUnpinnedSource, "artifact graph node %q has unpinned container %q", node.ID, node.Container)
			}
			if node.Platform != "" && node.Platform != "linux/amd64" && node.Platform != "linux/arm64" {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q has unsupported platform %q", node.ID, node.Platform)
			}
			if node.Kind != NodeBuild && (node.Requirements != nil || node.ExecutionBinding != nil) {
				return e.New(e.CodeInvalidCatalog,
					"artifact graph execution requirements and bindings are supported only on build node %q", node.ID)
			}
			if err := node.Requirements.Validate(); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err,
					"artifact graph node %q has invalid execution requirements", node.ID)
			}
			if node.ExecutionBinding != nil {
				if err := node.ExecutionBinding.ValidateFor(node.Requirements); err != nil {
					return e.Wrap(e.CodeInvalidCatalog, err,
						"artifact graph node %q has invalid execution binding", node.ID)
				}
			}
			switch node.Kind {
			case NodeFetch, NodeAcquire, NodePublish, NodeSign, NodeVerifyPublication, NodeAttestEvidence, NodeAssembleRelease:
				if node.Network != ir.NetworkDefault {
					return e.New(e.CodeInvalidCatalog, "artifact graph networked node %q does not declare default network", node.ID)
				}
			case NodeBuild:
				if node.Network != ir.NetworkNone && node.Network != ir.NetworkDefault {
					return e.New(e.CodeInvalidCatalog, "artifact graph build node %q has an unknown network mode", node.ID)
				}
			case NodeVerify, NodeSBOM, NodePackage, NodeInventory:
				if node.Network != ir.NetworkNone {
					return e.New(e.CodeInvalidCatalog, "artifact graph isolated node %q does not declare network none", node.ID)
				}
			default:
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q has unsupported kind %q", node.ID, node.Kind)
			}
			if node.Kind == NodeBuild {
				if node.IRNode == "" || node.Container == "" || node.Platform == "" {
					return e.New(e.CodeEngineCapability, "artifact graph build node %q lacks exact IR/container/platform execution inputs", node.ID)
				}
			}
			if node.Network == ir.NetworkNone && node.Kind != NodeBuild &&
				(node.Container == "" || node.Platform == "") {
				return e.New(e.CodeEngineCapability,
					"artifact graph isolated node %q lacks exact utility container/platform execution inputs", node.ID)
			}
			if node.Kind == NodeFetch && node.IRNode != "" && node.Container == "" {
				return e.New(e.CodeEngineCapability, "artifact graph fetch node %q has no pinned vendoring container", node.ID)
			}
			if node.Kind == NodeAcquire && node.IRNode == "" {
				if !strings.HasPrefix(node.URL, "https://") || !canonical.IsDigest(node.Digest) ||
					node.Executable == "" || !canonical.IsDigest(node.ExecutableDigest) ||
					(node.ToolFormat != ToolFormatRaw && node.ToolFormat != ToolFormatTarGzip) {
					return e.New(e.CodeUnpinnedSource, "artifact graph tool acquisition %q is not transport- and executable-pinned", node.ID)
				}
				if node.ToolFormat == ToolFormatRaw && node.Digest != node.ExecutableDigest {
					return e.New(e.CodeUnverifiedRef, "artifact graph raw tool acquisition %q has distinct transport and executable digests", node.ID)
				}
			}
		}
		if index > 0 && graph.Nodes[index-1].ID >= node.ID {
			return e.New(e.CodeInvalidCatalog, "artifact graph nodes are not canonical")
		}
		if len(node.Outputs) == 0 {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q has no named outputs", node.ID)
		}
		nodes[node.ID] = node
	}

	artifacts := make(map[string]Artifact, len(graph.Artifacts))
	for index, artifact := range graph.Artifacts {
		if !safeGraphID(artifact.ID) || artifacts[artifact.ID].ID != "" || artifact.Producer == "" {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifact %q is invalid or repeated", artifact.ID)
		}
		if !safeGeneratedPath(artifact.Path) {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifact %q has unsafe or empty path %q", artifact.ID, artifact.Path)
		}
		if index > 0 && graph.Artifacts[index-1].ID >= artifact.ID {
			return e.New(e.CodeInvalidCatalog, "artifact graph artifacts are not canonical")
		}
		if _, ok := nodes[artifact.Producer]; !ok {
			return e.New(e.CodeInvalidCatalog, "artifact %q names unknown producer %q", artifact.ID, artifact.Producer)
		}
		if artifact.BuildTarget != nodes[artifact.Producer].BuildTarget {
			return e.New(e.CodeUnverifiedRef, "artifact %q changes its producing output target", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}

	produced := map[string]string{}
	for _, node := range graph.Nodes {
		if !sort.StringsAreSorted(node.Inputs) || !sort.StringsAreSorted(node.Outputs) {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q inputs/outputs are not canonical", node.ID)
		}
		if hasAdjacentDuplicate(node.Inputs) || hasAdjacentDuplicate(node.Outputs) {
			return e.New(e.CodeInvalidCatalog, "artifact graph node %q repeats an input or output", node.ID)
		}
		for _, output := range node.Outputs {
			artifact, ok := artifacts[output]
			if !ok || artifact.Producer != node.ID || produced[output] != "" {
				return e.New(e.CodeInvalidCatalog, "artifact graph output %q has an invalid producer binding", output)
			}
			produced[output] = node.ID
		}
	}
	if len(produced) != len(artifacts) {
		return e.New(e.CodeInvalidCatalog, "artifact graph contains an artifact not declared by its producer")
	}
	for _, node := range graph.Nodes {
		for _, input := range node.Inputs {
			artifact, ok := artifacts[input]
			if !ok {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q consumes unknown artifact %q", node.ID, input)
			}
			if artifact.Producer == node.ID {
				return e.New(e.CodeInvalidCatalog, "artifact graph node %q consumes its own output %q", node.ID, input)
			}
		}
	}
	if len(topologicalFactoryNodes(graph)) != len(nodes) {
		return e.New(e.CodeInvalidCatalog, "artifact graph contains a dataflow cycle")
	}
	return nil
}

// topologicalFactoryNodes is shared by graph admission, local scheduling and
// hosted stage projection. It assumes valid node/artifact references and returns
// only reachable ready nodes, so callers detect cycles by checking the count.
func topologicalFactoryNodes(graph ArtifactGraph) []Node {
	byID := make(map[string]Node, len(graph.Nodes))
	producer := make(map[string]string, len(graph.Artifacts))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	for _, artifact := range graph.Artifacts {
		producer[artifact.ID] = artifact.Producer
	}
	indegree := make(map[string]int, len(graph.Nodes))
	children := make(map[string][]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		parents := map[string]bool{}
		for _, input := range node.Inputs {
			parent := producer[input]
			if !parents[parent] {
				parents[parent] = true
				indegree[node.ID]++
				children[parent] = append(children[parent], node.ID)
			}
		}
	}
	var ready []string
	for id := range byID {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	ordered := make([]Node, 0, len(byID))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	return ordered
}

func hasAdjacentDuplicate(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
}

func safeGraphID(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !canonicalName.MatchString(part) {
			return false
		}
	}
	return true
}
