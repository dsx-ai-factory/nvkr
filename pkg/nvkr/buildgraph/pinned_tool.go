// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// pinnedToolNode lowers a pinned-tool producer to a source-less KindBuild (ADR-27 §2).
// The tool pin's digests, member and format are logical identity; its release URL
// is transport. Tokens render into fixed sandbox paths owned by package ir.
func pinnedToolNode(nc catalog.NamedComponent, components map[string]catalog.NamedComponent, platform string) (ir.Node, error) {
	tool := nc.ToolExecutable
	if tool == nil || tool.Platform != platform {
		return ir.Node{}, e.New(e.CodeEngineCapability, "pinned-tool component %q has no tool pinned for target %q", nc.Name, platform)
	}
	output := ir.PinnedToolOutputDir
	if nc.OutputType == catalog.PinnedToolOutputOCILayout {
		output = ir.PinnedToolLayoutDir
	}
	command, err := catalog.RenderToolArgs(nc.ToolArgs, catalog.ToolArgValues{
		Output: output,
		Arch:   target.Arch(platform),
		Material: func(binding string) (string, error) {
			if _, ok := nc.Materials[binding]; !ok {
				return "", e.New(e.CodeInvalidCatalog, "pinned-tool component %q names undeclared material %q", nc.Name, binding)
			}
			path, err := material.StagingPath(binding)
			if err != nil {
				return "", err
			}
			return ir.PinnedToolInputsRoot + "/" + path, nil
		},
	})
	if err != nil {
		return ir.Node{}, e.Wrap(e.CodeInvalidCatalog, err, "pinned-tool component %q arguments", nc.Name)
	}
	materials := materialBindings(nc, components)
	var needs []string
	for _, binding := range materials {
		needs = append(needs, binding.Producer)
	}
	env := make([]ir.EnvVar, 0, len(nc.ToolEnv))
	for name, value := range nc.ToolEnv {
		env = append(env, ir.EnvVar{Name: name, Value: value})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
	return ir.Node{
		ID:           "build-" + nc.Name,
		Kind:         ir.KindBuild,
		Needs:        needs,
		Requirements: execution.Clone(nc.BuildRequirements),
		Component:    nc.Name,
		BuilderType:  ir.BuilderPinnedTool,
		Tool:         nc.Tool,
		Artifact:     tool.URL,
		Digest:       tool.ExecutableDigest,
		ExecutableArchive: &ir.ExecutableArchive{
			Format: tool.Format, Digest: tool.ArchiveDigest, Member: tool.ExecutablePath,
		},
		Toolchain:    nc.ToolchainImage,
		Platform:     platform,
		Network:      ir.NetworkNone,
		Env:          env,
		Command:      command,
		Workdir:      ir.PinnedToolWorkdir,
		Materials:    materials,
		OutputType:   nc.OutputType,
		ArtifactPath: nc.ArtifactPath,
		Version:      catalog.NormalizeVersion(nc.Tag),
	}, nil
}

// validatePinnedToolOutputs enforces the output rules of ADR-27 §5 before any
// acquisition or staging. An OCI layout reaches a Release only through the
// bundle-oci layout import. A file producer is admitted only as a material of another
// selected producer.
func validatePinnedToolOutputs(components []catalog.NamedComponent, format string) error {
	bound := make(map[string]bool)
	for _, component := range components {
		for _, name := range component.Materials {
			bound[name] = true
		}
	}
	for _, component := range components {
		if component.BuilderType != catalog.BuilderPinnedTool {
			continue
		}
		switch component.OutputType {
		case catalog.PinnedToolOutputOCILayout:
			if format != definition.OutputBundle {
				return e.New(e.CodeInvalidCatalog, "pinned-tool OCI layout producer %q requires a bundle-oci output", component.Name)
			}
		case catalog.PinnedToolOutputFile:
			if !bound[component.Name] {
				return e.New(e.CodeInvalidCatalog,
					"pinned-tool file producer %q is not bound as a material by another selected producer", component.Name)
			}
		}
	}
	return nil
}
