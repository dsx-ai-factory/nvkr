// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ArtifactTestComponent is the producer component whose test inputs validation
// admits. Selection below also accepts explicit CLI recipe variants.
const ArtifactTestComponent = "nvkectl"

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
	return selectCLIArtifactTestInputs(plan, output)
}

// CLIArtifactPlan retains one complete validated Plan for multi-check admission.
// Its producer graph is private and cannot be changed by the caller.
type CLIArtifactPlan struct {
	plan           *Plan
	documentSHA256 string
}

// LoadCLIArtifactPlan reads and validates the Plan once, before executable work.
func LoadCLIArtifactPlan(ctx context.Context, path string) (*CLIArtifactPlan, error) {
	plan, digest, err := loadPlanDocument(ctx, path)
	if err != nil {
		return nil, err
	}
	return &CLIArtifactPlan{plan: plan, documentSHA256: digest}, nil
}

// SelectCLIArtifactTestInputs derives the test inputs of one explicitly named
// CLI output from an already validated Plan, such as one replayed from a
// completed Run. It never infers the output, and refuses an output built from
// any component other than the named one.
func SelectCLIArtifactTestInputs(plan *Plan, output, component string) (*CLIArtifactTestInputs, error) {
	if plan == nil || output == "" || component == "" {
		return nil, e.New(e.CodeUsage, "CLI test input selection requires a Plan, an explicit output and a component")
	}
	inputs, err := selectCLIArtifactTestInputs(plan, output)
	if err != nil {
		return nil, err
	}
	// Selection admits CLI recipe variants; the caller names the one it accepts.
	for _, build := range plan.Builds {
		if build.Name == inputs.Output && len(build.Components) == 1 && build.Components[0] != component {
			return nil, e.New(e.CodeEngineCapability, "output %q is built from %q, not %q", output, build.Components[0], component)
		}
	}
	return inputs, nil
}

func selectCLIArtifactTestInputs(plan *Plan, output string) (*CLIArtifactTestInputs, error) {
	var selected *BuildPlan
	for i := range plan.Builds {
		build := &plan.Builds[i]
		if len(build.Components) != 1 || (output != "" && build.Name != output) {
			continue
		}
		// Catalog consolidation retains explicit CLI recipe variants. Names select
		// diagnostics only; the validated producer and toolchain checks below still apply.
		switch build.Components[0] {
		case "nvkectl", "nvkectl-module-tags", "nvkectl-local-site", "nvkectl-native-site":
		default:
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
	componentName := selected.Components[0]
	component := selected.CatalogSnapshot.Components[componentName]
	for _, output := range plan.Definition.Outputs {
		if output.Name == selected.Name && output.Format != "executable" {
			return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require an executable output")
		}
	}
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
		if node.Kind == ir.KindBuild && node.Component == componentName {
			if producer != nil {
				return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require one CLI build node")
			}
			producer = node
		}
	}
	if producer == nil || producer.BuilderType != ir.BuilderOCIHermetic || producer.Toolchain == "" || !component.FromSource || producer.OutputType != "host-package" {
		return nil, e.New(e.CodeEngineCapability, "CLI artifact tests require a source-built container toolchain")
	}
	classifier, err := plan.Environment.ToolchainAccess.ImageClassifier()
	if err != nil {
		return nil, err
	}
	if _, err := plan.resolveImageUse(classifier, "local", ImageUse{
		ConsumerID: selected.Name + "/diagnostic", Role: "diagnostic", Reference: producer.Toolchain,
		Platform: producer.ToolchainPlatform, Placement: "local",
	}, false); err != nil {
		return nil, err
	}
	return &CLIArtifactTestInputs{
		Output: selected.Name, BuildDigest: selected.BuildDigest, Platform: selected.Platform,
		Repo: component.Repo, Commit: component.Commit, TreeHash: component.TreeHash,
		ToolchainImage: producer.Toolchain, ExpectedVersion: version,
	}, nil
}
