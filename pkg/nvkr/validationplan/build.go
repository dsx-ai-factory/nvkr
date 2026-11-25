// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"context"
	stderrors "errors"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Request is everything Build needs besides the planners themselves.
type Request struct {
	// Release is the authenticated release the plan is for.
	Release Release

	// PlannersDir holds config.yaml and each planner's own files.
	PlannersDir string

	// Vars holds run-scoped values, keyed by planner name and then by key. A
	// planner only ever receives its own.
	Vars map[string]map[string]string
}

// Build runs every planner the configuration lists and joins their targets
// into one plan. Any failure returns a coded error and no plan.
//
// Build does not authenticate the release. The caller owns that, and Release is
// a description until it has.
func Build(ctx context.Context, req Request, planners map[string]Planner) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "validation planning requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation planning canceled")
	}
	if strings.TrimSpace(req.Release.Reference) == "" {
		return nil, e.New(e.CodeUsage, "validation planning requires a release reference")
	}
	cfg, err := LoadConfig(req.PlannersDir)
	if err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(cfg.Planners))
	for _, entry := range cfg.Planners {
		listed[entry.Name] = true
	}
	for name := range req.Vars {
		if !listed[name] {
			return nil, e.New(e.CodeUsage, "values were given for planner %q, which the configuration does not list", name)
		}
	}

	plan := &Plan{
		SchemaVersion:          SchemaVersion,
		QualificationAuthority: AuthorityNone,
		Release:                ReleaseRef{Reference: req.Release.Reference},
		Targets:                []Target{},
	}
	for _, entry := range cfg.Planners {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "validation planning canceled")
		}
		planner, ok := planners[entry.Name]
		if !ok {
			return nil, e.New(e.CodeUnsupported, "planner %q is not built in", entry.Name)
		}
		vars := make(map[string]string, len(req.Vars[entry.Name]))
		for k, v := range req.Vars[entry.Name] {
			vars[k] = v
		}
		result, err := planner.Plan(ctx, Input{
			Release: req.Release,
			Config:  filepath.Join(req.PlannersDir, filepath.FromSlash(entry.Config)),
			Vars:    vars,
		})
		if err != nil {
			return nil, coded(err, e.CodeInternal, "planner %q", entry.Name)
		}
		for _, target := range result.Targets {
			target.Executor = entry.Executor
			if target.Source.Planner != entry.Name {
				return nil, e.New(e.CodeInternal, "planner %q produced target %q attributed to %q", entry.Name, target.ID, target.Source.Planner)
			}
			if err := checkInputs(target); err != nil {
				return nil, err
			}
			plan.Targets = append(plan.Targets, target)
		}
		for _, embedded := range result.Plans {
			if embedded.Planner != entry.Name {
				return nil, e.New(e.CodeInternal, "planner %q embedded a plan attributed to %q", entry.Name, embedded.Planner)
			}
			plan.Plans = append(plan.Plans, embedded)
		}
	}
	sort.Slice(plan.Targets, func(i, j int) bool { return plan.Targets[i].ID < plan.Targets[j].ID })
	sort.Slice(plan.Plans, func(i, j int) bool { return plan.Plans[i].Planner < plan.Plans[j].Planner })
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// coded keeps a coded error as it is and gives any other error a code, so
// nothing crosses this package uncoded.
func coded(err error, code e.Code, format string, args ...any) error {
	var already *e.Error
	if stderrors.As(err, &already) {
		return err
	}
	return e.Wrap(code, err, format, args...)
}
