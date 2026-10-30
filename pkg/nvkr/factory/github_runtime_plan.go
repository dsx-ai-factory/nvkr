// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"slices"
	"strings"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// githubRuntimePlanBundle admits the reviewed workflows that refresh Plan data
// at dispatch: named production bundles and development overridable sources.
func githubRuntimePlanBundle(environment authored.Environment) bool {
	if !environment.UsesGitHubRuntimePlan() {
		return false
	}
	if environment.GitHubDevelopment {
		return environment.Overridable != nil && environment.GitHubBundle == ""
	}
	return environment.GitHubBundle != "" && environment.Overridable == nil
}

// PrepareGitHubRuntimeBundle admits a freshly compiled Plan against the complete
// committed bundle. Only Plan and descriptive action data may change; every
// workflow/action byte, member path, Environment and input selection stays fixed.
// This is generation admission, not a provider or publication authority receipt.
// The caller owns compilation from the exact current repository inputs.
func PrepareGitHubRuntimeBundle(ctx context.Context, freshPlan *Plan, root string, check bool) error {
	if ctx == nil || freshPlan == nil || strings.TrimSpace(root) == "" {
		return e.New(e.CodeUsage, "runtime GitHub preparation requires context, compiled Plan and repository root")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime GitHub preparation canceled")
	}
	if !githubRuntimePlanBundle(freshPlan.Environment) {
		return e.New(e.CodeEngineCapability, "runtime GitHub preparation requires an opted-in named production bundle or development overridable sources")
	}
	if err := freshPlan.Validate(); err != nil {
		return err
	}
	if freshPlan.DefinitionPath == "" || freshPlan.EnvironmentPath == "" {
		return e.New(e.CodeEngineCapability, "runtime GitHub preparation requires exact authored input paths")
	}
	layout := githubLayoutFor(freshPlan)
	baselinePath, err := generatedDestination(root, layout.planPath)
	if err != nil {
		return err
	}
	baselinePlan, err := LoadPlanContext(ctx, baselinePath)
	if err != nil {
		return err
	}
	baseline, err := RenderGitHub(ctx, baselinePlan)
	if err != nil {
		return err
	}
	// Validate all old bytes before considering the new pins. Stale authored pins
	// do not excuse edits to a committed Plan, descriptor, template or manifest.
	if err := CheckGeneratedBundle(ctx, root, baseline); err != nil {
		return err
	}
	if baselinePlan.EnvironmentDigest != freshPlan.EnvironmentDigest ||
		baselinePlan.DefinitionPath != freshPlan.DefinitionPath || baselinePlan.EnvironmentPath != freshPlan.EnvironmentPath {
		return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change Environment or authored input paths; regenerate the bundle")
	}
	fresh, err := RenderGitHub(ctx, freshPlan)
	if err != nil {
		return err
	}
	if !slices.Equal(sortedFileNames(baseline.Files), sortedFileNames(fresh.Files)) {
		return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change generated member paths; regenerate the bundle")
	}
	for _, member := range sortedFileNames(baseline.Files) {
		if member == layout.planPath || member == githubActionDataPathFor(freshPlan) {
			continue
		}
		if !bytes.Equal(baseline.Files[member], fresh.Files[member]) {
			return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change workflow or action %s; regenerate the bundle", member)
		}
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime GitHub preparation canceled before output")
	}
	if check {
		return nil
	}
	return WriteGeneratedBundle(ctx, root, fresh)
}

// LoadGeneratedGitHubPlan loads the Plan currently installed at the bundle path
// the given Plan selects. It is a description until the caller replays it.
func LoadGeneratedGitHubPlan(ctx context.Context, plan *Plan, root string) (*Plan, error) {
	if ctx == nil || plan == nil || strings.TrimSpace(root) == "" {
		return nil, e.New(e.CodeUsage, "loading a generated Plan requires context, Plan and repository root")
	}
	path, err := generatedDestination(root, githubLayoutFor(plan).planPath)
	if err != nil {
		return nil, err
	}
	return LoadPlanContext(ctx, path)
}
