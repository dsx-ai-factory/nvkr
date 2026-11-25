// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package validationplan describes what to validate for one authenticated
// release. A plan is a list of targets, each naming the executor that runs it
// and the inputs that executor takes, so independent workflow jobs can run the
// targets in parallel. Planners produce the targets and executors run them.
//
// A plan is a description in lane none. It issues no receipt, authenticates
// nothing about the release beyond what the caller already did, and gates no
// release.
package validationplan

import (
	"encoding/json"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SchemaVersion is the wire schema of a validation plan.
const SchemaVersion = "nvkr.dev/validation-plan/v1"

// AuthorityNone is the only value QualificationAuthority takes. A plan never
// carries qualification, manifest or release authority.
const AuthorityNone = "none"

// Plan is the validation plan document.
type Plan struct {
	SchemaVersion          string        `json:"schemaVersion"`
	QualificationAuthority string        `json:"qualificationAuthority"`
	Release                ReleaseRef    `json:"release"`
	Targets                []Target      `json:"targets"`
	Plans                  []PlannerPlan `json:"plans,omitempty"`
}

// ReleaseRef names the authenticated release the plan describes.
type ReleaseRef struct {
	Reference string `json:"reference"`
}

// Target is one unit of work that runs as its own workflow job.
type Target struct {
	// ID is unique in the plan and sorts the targets.
	ID string `json:"id"`

	// Executor names who runs the target. The builder copies it from the
	// planner's entry, so a planner never knows who executes its targets.
	Executor string `json:"executor"`

	Inputs Inputs `json:"inputs"`

	// Source says which planner produced the target and where to look in the
	// plan it embedded. Only that planner reads it.
	Source Source `json:"source"`
}

// Inputs are the values an executor consumes. They describe the work in terms
// any executor can take, and never in terms of the planner that produced it.
type Inputs struct {
	// Validator names the suite to run.
	Validator string `json:"validator,omitempty"`

	// Platform is the platform of the artifact a hermetic check runs against.
	Platform string `json:"platform,omitempty"`

	// TestEnv names the environment the suite runs in.
	TestEnv string `json:"testEnv,omitempty"`

	// Outputs maps what the suite calls an input to the release output that
	// supplies it. It names outputs and never carries a path or a digest, so
	// the executor selects them from the authenticated release.
	Outputs map[string]string `json:"outputs,omitempty"`
}

// Source is the planner's provenance for one target.
type Source struct {
	Planner     string `json:"planner"`
	PlanDigest  string `json:"planDigest,omitempty"`
	Combination string `json:"combination,omitempty"`
}

// PlannerPlan is a plan a planner made, embedded so the tools that read that
// planner's own format can use it. Plan is the planner's value in canonical
// encoding and Digest is its typed digest.
type PlannerPlan struct {
	Planner string          `json:"planner"`
	Digest  string          `json:"digest"`
	Plan    json.RawMessage `json:"plan"`
}

// NewPlannerPlan embeds value as the named planner's plan.
func NewPlannerPlan(planner string, value any) (PlannerPlan, error) {
	raw, err := canonical.Marshal(value)
	if err != nil {
		return PlannerPlan{}, err
	}
	digest, err := canonical.Digest(canonical.KindPlannerPlan, value)
	if err != nil {
		return PlannerPlan{}, err
	}
	return PlannerPlan{Planner: planner, Digest: digest, Plan: raw}, nil
}

// Validate checks the plan's shape. It performs no I/O.
func (p Plan) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return e.New(e.CodeUnsupported, "validation plan schema %q is unsupported", p.SchemaVersion)
	}
	if p.QualificationAuthority != AuthorityNone {
		return e.New(e.CodeUsage, "a validation plan carries no qualification authority")
	}
	if strings.TrimSpace(p.Release.Reference) == "" {
		return e.New(e.CodeUsage, "a validation plan names its release")
	}
	seen := make(map[string]bool, len(p.Targets))
	for _, t := range p.Targets {
		if t.ID == "" || strings.TrimSpace(t.ID) != t.ID {
			return e.New(e.CodeUsage, "a validation target requires an id")
		}
		if seen[t.ID] {
			return e.New(e.CodeUsage, "validation target %q appears more than once", t.ID)
		}
		seen[t.ID] = true
		if t.Executor == "" {
			return e.New(e.CodeUsage, "validation target %q names no executor", t.ID)
		}
		if t.Source.Planner == "" {
			return e.New(e.CodeUsage, "validation target %q names no planner", t.ID)
		}
	}
	embedded := make(map[string]bool, len(p.Plans))
	for _, pp := range p.Plans {
		if pp.Planner == "" || !canonical.IsDigest(pp.Digest) || len(pp.Plan) == 0 {
			return e.New(e.CodeUsage, "an embedded planner plan requires a planner, a digest and a plan")
		}
		if embedded[pp.Planner] {
			return e.New(e.CodeUsage, "planner %q embeds more than one plan", pp.Planner)
		}
		embedded[pp.Planner] = true
	}
	return nil
}

// Encode renders the plan in canonical encoding with a trailing newline.
func (p Plan) Encode() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(p)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
