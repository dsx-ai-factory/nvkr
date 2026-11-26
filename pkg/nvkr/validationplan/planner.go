// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"context"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Output is one authenticated output of the release under test.
type Output struct {
	Name      string
	Kind      string
	Reference string
	Digest    string
	Platform  string

	// Version is what a suite takes as the output's version, such as the tag
	// of the source it was built from. It is empty when the output has none.
	Version string

	// ComponentVersions maps a component of the output to the tag of the source
	// that built it, for an output whose own components come from sources at
	// different tags.
	ComponentVersions map[string]string
}

// Release is the authenticated release a planner reads. The caller
// authenticates it before a planner sees it.
type Release struct {
	Reference string
	Outputs   []Output
}

// Output returns the one output with the given name. A name that matches none
// or more than one is a coded error.
func (r Release) Output(name string) (Output, error) {
	var found []Output
	for _, o := range r.Outputs {
		if o.Name == name {
			found = append(found, o)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Output{}, e.New(e.CodeNoMatch, "the release has no output named %q", name)
	default:
		return Output{}, e.New(e.CodeAmbiguous, "the release has %d outputs named %q", len(found), name)
	}
}

// Input is what a planner receives.
type Input struct {
	Release Release

	// Config is the path of the planner's own configuration file. Relative
	// paths inside it resolve beside it.
	Config string

	// Vars holds the run-scoped values passed for this planner only.
	Vars map[string]string
}

// Result is what a planner returns. The builder fills each target's executor.
type Result struct {
	Targets []Target
	Plans   []PlannerPlan
}

// Planner turns an authenticated release and its own configuration into
// targets. It runs nothing, claims no authority, and is a deterministic
// function of its input.
type Planner interface {
	Plan(ctx context.Context, in Input) (Result, error)
}

// RejectUnknownVars fails when vars holds a key outside allowed, so a misspelled
// key cannot read as an omission.
func RejectUnknownVars(planner string, vars map[string]string, allowed ...string) error {
	var unknown []string
	for k := range vars {
		known := false
		for _, a := range allowed {
			if k == a {
				known = true
				break
			}
		}
		if !known {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return e.New(e.CodeUsage, "planner %q does not take %s", planner, strings.Join(unknown, ", "))
}
