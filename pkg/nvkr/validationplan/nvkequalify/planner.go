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
	"strconv"
	"strings"

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

	roleVersions, err := roleVersions(registry, in.Release)
	if err != nil {
		return validationplan.Result{}, err
	}
	combination := plan.Combinations[0]
	targets := make([]validationplan.Target, 0, len(combination.Streams))
	for _, stream := range combination.Streams {
		components, err := streamComponents(combination, stream, roleVersions)
		if err != nil {
			return validationplan.Result{}, err
		}
		targets = append(targets, validationplan.Target{
			ID: targetPrefix + stream.Name,
			Inputs: validationplan.Inputs{
				Suite:        cfg.Suite,
				TestEnv:      stream.Name,
				Capabilities: cloneCapabilities(stream.Capabilities),
				TestParams:   testParams(stream.Parameters),
				Components:   components,
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

// cloneCapabilities copies the stream's capabilities, so no two targets share
// a map.
func cloneCapabilities(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// testParams states the stream's run settings by NVKE's own names. A setting
// the policy leaves unset is absent, which leaves the run as it is.
func testParams(p qualification.Parameters) map[string]string {
	out := map[string]string{}
	if p.Environment != "" {
		out["environment"] = p.Environment
	}
	if p.TenantWorkers != 0 {
		out["tenantWorkers"] = strconv.Itoa(p.TenantWorkers)
	}
	if p.SuiteTimeoutMinutes != 0 {
		out["suiteTimeoutMinutes"] = strconv.Itoa(p.SuiteTimeoutMinutes)
	}
	if p.TestGridGroup != "" {
		out["testGridGroup"] = p.TestGridGroup
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// roleVersions gives each registry component that names a versionComponent the
// tag of the source that built that component of its release output. NVKE's
// resolution leaves such a component with a location and no version, so the
// version is read from the release. It fails when the release has no tag for
// the named component.
func roleVersions(registry *qualification.Registry, release validationplan.Release) (map[string]string, error) {
	versions := map[string]string{}
	for _, c := range registry.Spec.Components {
		rule := c.Resolve.Location
		if rule == nil || rule.Strategy != StrategyLocation || rule.With[withVersionComponent] == "" {
			continue
		}
		output, err := release.Output(rule.With[withOutput])
		if err != nil {
			return nil, e.Wrap(e.CodeUsage, err, "resolving the version of %s", c.Name)
		}
		tag := output.ComponentVersions[rule.With[withVersionComponent]]
		if tag == "" {
			return nil, e.New(e.CodeUsage, "%s takes its version from component %q of output %q, which was built from no tagged source", c.Name, rule.With[withVersionComponent], output.Name)
		}
		versions[c.Name] = tag
	}
	return versions, nil
}

// streamComponents lists everything one stream's suite is given: the
// combination's components, the dependencies shared by every stream, and the
// ones that depend on this stream. It fails closed on a stream that gets
// nothing from the release, and on a name that appears twice.
func streamComponents(combination qualification.Combination, stream qualification.Stream, roleVersions map[string]string) (map[string]validationplan.Component, error) {
	components := map[string]validationplan.Component{}
	fromRelease := false
	for _, list := range [][]qualification.ComponentVersion{combination.Components, combination.Dependencies, stream.Dependencies} {
		for _, v := range list {
			if _, dup := components[v.Name]; dup {
				return nil, e.New(e.CodeInvalidCatalog, "%s resolved component %q more than once for stream %q", Name, v.Name, stream.Name)
			}
			c := component(v)
			if tag, ok := roleVersions[v.Name]; ok {
				c.Version = tag
			}
			fromRelease = fromRelease || c.Origin == validationplan.OriginRelease
			components[v.Name] = c
		}
	}
	if !fromRelease {
		return nil, e.New(e.CodeInvalidCatalog, "stream %q takes nothing from the release", stream.Name)
	}
	return components, nil
}

// component converts one resolved value. An https location can only come from
// an override, which names where a person served a copy, so the release does
// not vouch for it. A release reference carries the digest the pull checks.
func component(v qualification.ComponentVersion) validationplan.Component {
	c := validationplan.Component{Version: v.Version, Origin: validationplan.OriginResolved}
	servedElsewhere := false
	switch {
	case strings.HasPrefix(v.URL, "https://"):
		c.URL = v.URL
		servedElsewhere = true
	case v.URL != "":
		c.Reference = v.URL
	}
	switch {
	case servedElsewhere:
	case v.VersionStrategy == StrategyVersion || v.LocationStrategy == StrategyLocation:
		c.Origin = validationplan.OriginRelease
	case v.VersionStrategy == StrategyDeclared:
		c.Origin = validationplan.OriginDeclared
	}
	return c
}

var _ validationplan.Planner = Planner{}
