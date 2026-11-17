// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// PrepareGitHubRuntimeBundle admits a freshly compiled Plan against the complete
// committed bundle. Only the Plan and derived manifest may change; every
// workflow/action byte, script binding, Environment and input selection stays fixed.
// This is generation admission, not a provider or publication authority receipt.
// The caller owns compilation from the exact current repository inputs.
func PrepareGitHubRuntimeBundle(ctx context.Context, freshPlan *Plan, root string, check bool) error {
	if ctx == nil || freshPlan == nil || strings.TrimSpace(root) == "" {
		return e.New(e.CodeUsage, "runtime GitHub preparation requires context, compiled Plan and repository root")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime GitHub preparation canceled")
	}
	if !freshPlan.Environment.GitHubRuntimePlan || freshPlan.Environment.GitHubBundle == "" || freshPlan.Environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "runtime GitHub preparation requires an opted-in named production bundle")
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
	// Validate all old bytes before considering the new pins. Stale authored pins
	// do not excuse edits to a committed Plan, descriptor, template or manifest.
	baseline, err := checkCoreGitHubBundle(ctx, baselinePlan, root)
	if err != nil {
		return err
	}
	fresh, err := RenderGitHub(ctx, freshPlan)
	if err != nil {
		return err
	}
	return prepareGitHubRuntimeBundle(ctx, baselinePlan, freshPlan, baseline, fresh, root, check)
}

// Callers supply a baseline already checked against disk and a freshly rendered
// bundle. Reuse those exact bytes rather than repeating either projection.
func prepareGitHubRuntimeBundle(ctx context.Context, baselinePlan, freshPlan *Plan, baseline, fresh *GeneratedBundle, root string, check bool) error {
	layout := githubLayoutFor(freshPlan)
	if baselinePlan.EnvironmentDigest != freshPlan.EnvironmentDigest ||
		baselinePlan.DefinitionPath != freshPlan.DefinitionPath || baselinePlan.EnvironmentPath != freshPlan.EnvironmentPath {
		return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change Environment or authored input paths; regenerate the bundle")
	}
	if freshPlan.RuntimeInputs != nil {
		if err := CheckFrozenAuthoredInputs(ctx, freshPlan, root); err != nil {
			return err
		}
	}
	beforeBindings, err := runtimeScriptBindings(baselinePlan)
	if err != nil {
		return err
	}
	afterBindings, err := runtimeScriptBindings(freshPlan)
	if err != nil {
		return err
	}
	if !bytes.Equal(beforeBindings, afterBindings) {
		return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change target or script bindings; regenerate the bundle")
	}
	staticMembers := func(bundle *GeneratedBundle) []string {
		return slices.DeleteFunc(sortedFileNames(bundle.Files), isPlanStoreRecord)
	}
	if !slices.Equal(staticMembers(baseline), staticMembers(fresh)) {
		return e.New(e.CodePipelineDrift, "runtime GitHub preparation cannot change generated member paths; regenerate the bundle")
	}
	for _, member := range sortedFileNames(baseline.Files) {
		if member == layout.planPath || isPlanStoreRecord(member) {
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

// Content addresses change with pins. Bind scripts to logical targets and their
// consumers independently of those addresses so cohort splits still require
// regeneration even when both cohorts execute identical script bytes.
func runtimeScriptBindings(plan *Plan) ([]byte, error) {
	type script struct {
		Field     string   `json:"field"`
		Consumers []string `json:"consumers"`
		Operand   int      `json:"operand"`
		Body      string   `json:"body"`
	}
	type target struct {
		Name     string   `json:"name"`
		Platform string   `json:"platform"`
		Scripts  []string `json:"scripts"`
	}
	var targets []target
	for _, build := range plan.Builds {
		entry := target{Name: build.Name, Platform: build.Platform}
		for _, node := range build.Graph.Nodes {
			for _, command := range []struct {
				field string
				argv  []string
			}{{"command", node.Command}, {"vendorCmd", node.VendorCmd}} {
				consumers := []string{node.Component}
				if command.field == "vendorCmd" {
					consumers = nil
					for _, consumer := range build.Graph.Nodes {
						if consumer.Component != "" && slices.Contains(consumer.Needs, node.ID) {
							consumers = append(consumers, consumer.Component)
						}
					}
					slices.Sort(consumers)
				}
				for i, operand := range command.argv {
					if !strings.Contains(operand, "\n") {
						continue
					}
					raw, err := canonical.Marshal(script{command.field, consumers, i, operand})
					if err != nil {
						return nil, err
					}
					entry.Scripts = append(entry.Scripts, string(raw))
				}
			}
		}
		slices.Sort(entry.Scripts)
		targets = append(targets, entry)
	}
	return canonical.Marshal(targets)
}
