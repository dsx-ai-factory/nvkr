// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// CLIArtifactTestInputs describes a validated Plan's CLI test runner inputs.
// It authenticates neither supplied artifact bytes nor membership in a Run.
type CLIArtifactTestInputs struct {
	Output          string `json:"output"`
	BuildDigest     string `json:"buildDigest"`
	Platform        string `json:"platform"`
	Repo            string `json:"repo"`
	Commit          string `json:"commit"`
	TreeHash        string `json:"treeHash"`
	ToolchainImage  string `json:"toolchainImage"`
	ExpectedVersion string `json:"expectedVersion"`
}

// LoadCLIArtifactTestInputs validates the complete generated Plan before selecting
// a standalone CLI producer. The compiled node owns native toolchain selection
// and permitted registry relocation; the diagnostic must not reconstruct either.
func LoadCLIArtifactTestInputs(ctx context.Context, path, output string) (*CLIArtifactTestInputs, error) {
	plan, err := LoadPlanContext(ctx, path)
	if err != nil {
		return nil, err
	}
	var selected *BuildPlan
	for i := range plan.Builds {
		build := &plan.Builds[i]
		if len(build.Components) != 1 || build.Components[0] != "nvkectl" || (output != "" && build.Name != output) {
			continue
		}
		if selected != nil {
			return nil, e.New(e.CodeUsage, "multiple standalone CLI outputs; select --output")
		}
		selected = build
	}
	if selected == nil {
		return nil, e.New(e.CodeUsage, "no matching standalone CLI output")
	}
	if selected.Platform != "linux/amd64" && selected.Platform != "linux/arm64" {
		return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require Linux AMD64 or ARM64")
	}
	component := selected.CatalogSnapshot.Components["nvkectl"]
	version := ""
	for _, key := range []string{"NVKECTL_VERSION", "NKECTL_VERSION"} {
		value, present := component.VersionStamp[key]
		if !present {
			continue
		}
		if strings.TrimSpace(value) == "" || (version != "" && version != value) {
			return nil, e.New(e.CodeInvalidCatalog, "CLI version stamps are empty or conflicting")
		}
		version = value
	}
	if version == "" {
		return nil, e.New(e.CodeInvalidCatalog, "CLI version stamp is required")
	}
	var producer *ir.Node
	for i := range selected.Graph.Nodes {
		node := &selected.Graph.Nodes[i]
		if node.Kind == ir.KindBuild && node.Component == "nvkectl" {
			if producer != nil {
				return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require one CLI build node")
			}
			producer = node
		}
	}
	if producer == nil || producer.BuilderType != ir.BuilderOCIHermetic || producer.Toolchain == "" || !component.FromSource {
		return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require a source-built container toolchain")
	}
	return &CLIArtifactTestInputs{
		Output: selected.Name, BuildDigest: selected.BuildDigest, Platform: selected.Platform,
		Repo: component.Repo, Commit: component.Commit, TreeHash: component.TreeHash,
		ToolchainImage: producer.Toolchain, ExpectedVersion: version,
	}, nil
}
