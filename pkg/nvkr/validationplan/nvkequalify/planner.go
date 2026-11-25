// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nvkequalify plans end-to-end targets with NVKE's qualification
// planner. It is the only package that imports NVKE's release module, and it
// reads NVKE's three documents (subject, policy and registry) from NVKR's own
// authored inputs.
//
// A component the authenticated release supplies resolves through the two
// strategies in resolver.go. NVKE's built-in strategies resolve the rest, from
// an NVKE tree the caller names.
package nvkequalify

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvke/release/pkg/qualification"
	"github.com/dsx-ai-factory/nvke/release/pkg/resolve"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
)

const (
	// Name is the planner's name in a planners configuration.
	Name = "nvke-qualify"

	// VarNVKERoot is the run-scoped variable naming an NVKE tree. NVKE's
	// built-in strategies read their pins from it.
	VarNVKERoot = "nvke-root"

	// VarOverrides is the run-scoped variable holding overrides in the form
	// NVKE's qualify plan takes: comma-separated name=value pairs, with no comma
	// inside a value. NVKE checks each name against the subject.
	VarOverrides = "overrides"

	targetPrefix = "e2e/"
)

// Planner plans for NVKE's end-to-end suite. It keeps no state.
type Planner struct{}

// Plan resolves the release into one NVKE plan and one target for each stream
// in it. It fails closed on a plan with more than one combination, which none
// of the maintained subjects can produce.
func (Planner) Plan(ctx context.Context, in validationplan.Input) (validationplan.Result, error) {
	if err := ctx.Err(); err != nil {
		return validationplan.Result{}, e.Wrap(e.CodeTimeout, err, "%s planning canceled", Name)
	}
	cfg, paths, err := loadConfig(in.Config)
	if err != nil {
		return validationplan.Result{}, err
	}
	root, overrides, err := splitVars(in.Vars)
	if err != nil {
		return validationplan.Result{}, err
	}

	policy, err := qualification.LoadPolicy(paths.policy)
	if err != nil {
		return validationplan.Result{}, e.Wrap(e.CodeUsage, err, "loading the %s policy", Name)
	}
	registry, err := qualification.LoadRegistry(paths.registry)
	if err != nil {
		return validationplan.Result{}, e.Wrap(e.CodeUsage, err, "loading the %s registry", Name)
	}
	versions, locations, err := qualification.BuildResolvers(registry, root, resolve.Options{}, strategies(in.Release))
	if err != nil {
		return validationplan.Result{}, e.Wrap(e.CodeUsage, err, "building the %s resolvers", Name)
	}

	planner := &qualification.Planner{
		Policy:            policy,
		Registry:          registry,
		VersionResolvers:  versions,
		LocationResolvers: locations,
		Root:              root,
		SubjectFile:       paths.subject,
		Overrides:         overrides,
		// The subject's version is the release digest, which names no commit in
		// the NVKE tree, so components are resolved from their own pins.
		ResolveAtCommit: false,
	}
	plan, err := planner.Plan(cfg.Subject, "", cfg.Scope)
	if err != nil {
		return validationplan.Result{}, e.Wrap(e.CodeUsage, err, "planning %s with %s", cfg.Subject, Name)
	}
	if len(plan.Combinations) != 1 {
		return validationplan.Result{}, e.New(e.CodeUnsupported, "%s resolved %d combinations, and only one is supported", Name, len(plan.Combinations))
	}

	embedded, err := validationplan.NewPlannerPlan(Name, plan)
	if err != nil {
		return validationplan.Result{}, err
	}
	outputs, err := releaseOutputs(registry)
	if err != nil {
		return validationplan.Result{}, err
	}

	combination := plan.Combinations[0]
	targets := make([]validationplan.Target, 0, len(combination.Streams))
	for _, stream := range combination.Streams {
		copied := make(map[string]string, len(outputs))
		for k, v := range outputs {
			copied[k] = v
		}
		targets = append(targets, validationplan.Target{
			ID: targetPrefix + stream.Name,
			Inputs: validationplan.Inputs{
				Validator: cfg.Validator,
				TestEnv:   stream.Name,
				Outputs:   copied,
			},
			Source: validationplan.Source{Planner: Name, PlanDigest: embedded.Digest, Combination: combination.ID},
		})
	}
	return validationplan.Result{Targets: targets, Plans: []validationplan.PlannerPlan{embedded}}, nil
}

// splitVars reads the two variables the planner takes and rejects any other,
// so a misspelled name cannot read as an omission. Overrides keep NVKE's own
// grammar and are parsed by NVKE's parser.
func splitVars(vars map[string]string) (root string, overrides map[string]string, err error) {
	if err := validationplan.RejectUnknownVars(Name, vars, VarNVKERoot, VarOverrides); err != nil {
		return "", nil, err
	}
	root = vars[VarNVKERoot]
	if root == "" {
		return "", nil, e.New(e.CodeUsage, "planner %q requires --planner-var %s.%s=<path to an NVKE tree>", Name, Name, VarNVKERoot)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", nil, e.Wrap(e.CodeUsage, err, "resolving the NVKE tree")
	}
	info, statErr := os.Stat(root)
	if statErr != nil || !info.IsDir() {
		return "", nil, e.New(e.CodeUsage, "the NVKE tree %q is not a directory", root)
	}
	overrides, err = qualification.ParseOverrides(vars[VarOverrides])
	if err != nil {
		return "", nil, e.Wrap(e.CodeUsage, err, "the %s variable of planner %q", VarOverrides, Name)
	}
	return root, overrides, nil
}

// releaseOutputs maps each registry component the release supplies to the
// output that supplies it, in the terms a target's inputs use.
func releaseOutputs(registry *qualification.Registry) (map[string]string, error) {
	outputs := map[string]string{}
	for _, component := range registry.Spec.Components {
		for _, rule := range []struct {
			strategy string
			rule     *qualification.Rule
		}{
			{StrategyVersion, component.Resolve.Version},
			{StrategyLocation, component.Resolve.Location},
		} {
			if rule.rule == nil || rule.rule.Strategy != rule.strategy || rule.rule.With[withOutput] == "" {
				continue
			}
			output := rule.rule.With[withOutput]
			if previous, ok := outputs[component.Name]; ok && previous != output {
				return nil, e.New(e.CodeInvalidCatalog, "registry component %q takes its version from output %q and its location from %q", component.Name, previous, output)
			}
			outputs[component.Name] = output
		}
	}
	if len(outputs) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "the %s registry names no release output", Name)
	}
	return outputs, nil
}

var _ validationplan.Planner = Planner{}
