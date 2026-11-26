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
	"fmt"
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
	SchemaVersion          string     `json:"schemaVersion"`
	QualificationAuthority string     `json:"qualificationAuthority"`
	Release                ReleaseRef `json:"release"`
	Targets                []Target   `json:"targets"`

	// PlannerPlans holds each planner's own plan. They are written beside the
	// plan and are no part of its encoding. A target's source names the digest
	// of the one it came from.
	PlannerPlans []PlannerPlan `json:"-"`
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
	// plan it made. Only that planner reads it.
	Source Source `json:"source"`
}

// Inputs are the values an executor consumes. They describe the work in terms
// any executor can take, and never in terms of the planner that produced it.
type Inputs struct {
	// Suite names the suite to run.
	Suite string `json:"suite,omitempty"`

	// Platform is the platform of the artifact a hermetic check runs against.
	Platform string `json:"platform,omitempty"`

	// TestEnv names the environment the suite runs in.
	TestEnv string `json:"testEnv,omitempty"`

	// Capabilities describe the environment, such as its provider, GPU model,
	// architecture, operating system and Kubernetes minor. The planner copies
	// them from its own plan, so a job needs no other document to know them.
	Capabilities map[string]string `json:"capabilities,omitempty"`

	// TestParams are the settings the environment gives the run, as strings
	// in the planner's own vocabulary. An unset setting is absent.
	TestParams map[string]string `json:"testParams,omitempty"`

	// Components maps each name the suite knows a component by to what the
	// executor needs to fetch and configure it. Everything is resolved here, so
	// an executor never reads the release again.
	Components map[string]Component `json:"components,omitempty"`
}

// Origin says who vouches for a component's values.
const (
	// OriginRelease marks values read from the authenticated release. A
	// reference carries the digest the pull authenticates.
	OriginRelease = "release"

	// OriginDeclared marks a version a reviewer wrote down in authored input.
	// Nothing checks it against the bytes under test.
	OriginDeclared = "declared"

	// OriginResolved marks a value the planner's own strategies resolved, from
	// pins in a source tree for example. The release does not vouch for it.
	OriginResolved = "resolved"
)

// Component is one thing a suite is given, whether it is built bytes, a served
// file or a bare version. Each field is optional because a component carries
// only the ones that apply to it.
type Component struct {
	// Version is the value the suite takes for the component.
	Version string `json:"version,omitempty"`

	// Reference is a registry reference. For a release component it names the
	// output by digest.
	Reference string `json:"reference,omitempty"`

	// URL is an https location, such as where a node image is served.
	URL string `json:"url,omitempty"`

	// Origin says who vouches for the values above.
	Origin string `json:"origin"`
}

// Source is the planner's provenance for one target.
type Source struct {
	Planner     string `json:"planner"`
	PlanDigest  string `json:"planDigest,omitempty"`
	Combination string `json:"combination,omitempty"`
}

// PlannerPlan is a plan a planner made, kept so the tools that read that
// planner's own format can use it. Plan is the planner's value in canonical
// encoding and Digest is its typed digest, which is not a hash of the file.
// Recompute it by decoding the file and canonicalizing the value.
type PlannerPlan struct {
	Planner string          `json:"planner"`
	Digest  string          `json:"digest"`
	Plan    json.RawMessage `json:"plan"`
}

// NewPlannerPlan records value as the named planner's plan.
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

func (c Component) validate() error {
	switch c.Origin {
	case OriginRelease, OriginDeclared, OriginResolved:
	default:
		return fmt.Errorf("origin %q is not one of %s, %s or %s", c.Origin, OriginRelease, OriginDeclared, OriginResolved)
	}
	if c.Version == "" && c.Reference == "" && c.URL == "" {
		return fmt.Errorf("carries no version, reference or url")
	}
	return nil
}

// FileName is the name the planner's plan is written under.
func (pp PlannerPlan) FileName() string { return pp.Planner + ".json" }

// Encode renders the planner's plan as it is written to its file.
func (pp PlannerPlan) Encode() []byte { return append(append([]byte{}, pp.Plan...), '\n') }

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
		for name, c := range t.Inputs.Components {
			if err := c.validate(); err != nil {
				return e.Wrap(e.CodeUsage, err, "validation target %q component %q", t.ID, name)
			}
		}
	}
	kept := make(map[string]bool, len(p.PlannerPlans))
	for _, pp := range p.PlannerPlans {
		if pp.Planner == "" || !canonical.IsDigest(pp.Digest) || len(pp.Plan) == 0 {
			return e.New(e.CodeUsage, "a planner plan requires a planner, a digest and a plan")
		}
		if kept[pp.Planner] {
			return e.New(e.CodeUsage, "planner %q made more than one plan", pp.Planner)
		}
		kept[pp.Planner] = true
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
