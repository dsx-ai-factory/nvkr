// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"net/url"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// withProducerTools adds the tools that selected pinned-tool producers run to the
// run-wide tool selection (ADR-27 §2). One tool name identifies one tool/<name>
// acquisition, so every selection of a name, run-wide or producer, must carry the
// identical pin.
func withProducerTools(tools map[string]catalog.ToolPin, builds []BuildPlan) (map[string]catalog.ToolPin, error) {
	merged := maps.Clone(tools)
	if merged == nil {
		merged = map[string]catalog.ToolPin{}
	}
	for _, build := range builds {
		if build.Graph == nil {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild || node.BuilderType != ir.BuilderPinnedTool {
				continue
			}
			pin, err := pinnedToolPin(node)
			if err != nil {
				return nil, err
			}
			if previous, exists := merged[node.Tool]; exists {
				// The shared acquisition stages only the executable; companions are delivery-only.
				previous.Companions = nil
				if !reflect.DeepEqual(previous, pin) {
					return nil, e.New(e.CodeUnpinnedSource, "catalog tool %q is selected with conflicting pins", node.Tool)
				}
			}
			merged[node.Tool] = pin
		}
	}
	return merged, nil
}

// pinnedToolPin projects the tool pin a pinned-tool build carries in its IR node.
// The shared acquisition supports raw and tar-gzip assets only.
func pinnedToolPin(node ir.Node) (catalog.ToolPin, error) {
	archive := node.ExecutableArchive
	if archive == nil || node.Tool == "" {
		return catalog.ToolPin{}, e.New(e.CodePipelineDrift, "pinned-tool build %q has no tool pin", node.ID)
	}
	if archive.Format != ToolFormatRaw && archive.Format != ToolFormatTarGzip {
		return catalog.ToolPin{}, e.New(e.CodeEngineCapability,
			"pinned-tool build %q tool format %q is not supported by tool acquisition", node.ID, archive.Format)
	}
	return catalog.ToolPin{Format: archive.Format, URL: node.Artifact, Platform: node.Platform,
		ExecutablePath: archive.Member, ArchiveDigest: archive.Digest, ExecutableDigest: node.Digest}, nil
}

// pinnedToolDependencies records the tool exactly as a selected release executable
// (ADR-27 §7): the release URL bound to the archive digest, and a logical
// nvkr:tool/<name> URI naming the archive and member, bound to the executable digest.
func pinnedToolDependencies(node ir.Node) []provenance.Dependency {
	logical := "nvkr:tool/" + url.PathEscape(node.Tool) + "?" + url.Values{
		"archive": {node.Artifact}, "member": {node.ExecutableArchive.Member},
	}.Encode()
	return []provenance.Dependency{
		{URI: node.Artifact, Digest: map[string]string{"sha256": strings.TrimPrefix(node.ExecutableArchive.Digest, "sha256:")}},
		{URI: logical, Digest: map[string]string{"sha256": strings.TrimPrefix(node.Digest, "sha256:")}},
	}
}

// requirePinnedToolDependencies rejects production provenance that omits or alters
// the tool dependencies of any pinned-tool build in the plan.
func requirePinnedToolDependencies(plan *Plan, dependencies []provenance.Dependency) error {
	recorded := make(map[string]string, len(dependencies))
	for _, dependency := range dependencies {
		recorded[dependency.URI] = dependency.Digest["sha256"]
	}
	for _, build := range plan.Builds {
		if build.Graph == nil {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild || node.BuilderType != ir.BuilderPinnedTool {
				continue
			}
			if node.ExecutableArchive == nil {
				return e.New(e.CodeProvenanceMissing, "pinned-tool build %q has no tool pin to record", node.ID)
			}
			for _, want := range pinnedToolDependencies(node) {
				if got, ok := recorded[want.URI]; !ok || got != want.Digest["sha256"] {
					return e.New(e.CodeProvenanceMissing, "production provenance omits or alters tool dependency %q", want.URI)
				}
			}
		}
	}
	return nil
}

// admitLocalPinnedTools refuses a local Run whose host cannot execute every selected
// pinned-tool build, before any acquisition or staging (ADR-27 §8).
func admitLocalPinnedTools(plan *Plan) error {
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeBuild || node.IRNode == "" {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return err
		}
		if instruction.BuilderType != ir.BuilderPinnedTool {
			continue
		}
		if err := pipeline.AdmitPinnedToolHost(instruction); err != nil {
			return err
		}
	}
	return nil
}
