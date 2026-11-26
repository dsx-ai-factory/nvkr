// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"context"
	stderrors "errors"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// GenerateRequest names the release to plan for and where the planners live.
type GenerateRequest struct {
	// Reference is the immutable release, repository@sha256:digest.
	Reference string

	// Policy is the qualification policy that authenticates the release.
	Policy string

	PlannersDir string
	Vars        map[string]map[string]string
}

// readFunc reads what the release was built from and what it published with
// the fast route: the signed root and the producing attempt's handoff, and
// nothing else. The lifecycle owner satisfies it.
type readFunc func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (lifecycle.ReleaseBuild, error)

// Generate authenticates the release root under the policy, reads the release's
// outputs and the versions of the sources they were built from, and builds the
// plan. It does not verify each output's signature or evidence. The root binds
// every output's reference and digest, and a pull by digest authenticates the
// bytes an executor fetches.
func Generate(ctx context.Context, req GenerateRequest, planners map[string]Planner, options lifecycle.QualificationRuntimeOptions) (*Plan, error) {
	return generate(ctx, req, planners, options, lifecycle.ReadReleaseBuild)
}

func generate(ctx context.Context, req GenerateRequest, planners map[string]Planner, options lifecycle.QualificationRuntimeOptions, read readFunc) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "validation planning requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation planning canceled")
	}
	for name, value := range map[string]string{"release reference": req.Reference, "qualification policy": req.Policy, "planners directory": req.PlannersDir} {
		if strings.TrimSpace(value) == "" {
			return nil, e.New(e.CodeUsage, "validation planning requires a %s", name)
		}
	}
	if strings.Contains(req.Reference, "://") {
		return nil, e.New(e.CodeUnverifiedRef, "validation planning requires an OCI release reference, not a URL")
	}
	if err := delivery.ValidatePublicationReference(req.Reference); err != nil {
		return nil, err
	}
	// The planners and their configuration are admitted before the registry is
	// reached, so a bad directory fails without any provider work.
	if _, err := LoadConfig(req.PlannersDir); err != nil {
		return nil, err
	}

	build, err := read(ctx, req.Reference, req.Policy, options)
	if err != nil {
		return nil, err
	}
	release, err := releaseFrom(build, req.Reference)
	if err != nil {
		return nil, err
	}
	return Build(ctx, Request{Release: release, PlannersDir: req.PlannersDir, Vars: req.Vars}, planners)
}

// releaseFrom reads the release a planner sees out of what the release was built
// from and what it published. The Plan's own release must be the one that was
// asked for, so a plan is never made for a different release.
//
// It joins what the release published with what it was built from. Each
// published output takes the tag of the source that built it, and the tag of
// each of its components, from the Plan's sources.
func releaseFrom(build lifecycle.ReleaseBuild, requested string) (Release, error) {
	built := build.Plan
	if built.Release == "" {
		return Release{}, e.New(e.CodeProvenanceMissing, "the release build names no release")
	}
	_, want, _ := strings.Cut(requested, "@")
	_, got, _ := strings.Cut(built.Release, "@")
	if want == "" || want != got {
		return Release{}, e.New(e.CodeVerifyFailed, "the described release differs from the requested reference")
	}
	outputs := make([]Output, 0, len(build.Outputs))
	for _, o := range build.Outputs {
		if o.Name == "" || !canonical.IsDigest(o.Digest) || o.Reference == "" {
			return Release{}, e.New(e.CodeProvenanceMissing, "release output %q lacks a name, a reference or a digest", o.Name)
		}
		outputs = append(outputs, Output{
			Name: o.Name, Kind: o.Kind, Reference: o.Reference, Digest: o.Digest, Platform: o.Platform,
			Version:           outputVersion(o.Components, built.Sources),
			ComponentVersions: componentVersions(o.Components, built.Sources),
		})
	}
	return Release{Reference: built.Release, Outputs: outputs}, nil
}

// LocalPrefix starts every reference in a plan made from a local Run. Such a
// reference names no registry location and cannot be fetched.
const LocalPrefix = "local/"

// LocalRequest names a complete local Run to plan for and where the planners
// live.
type LocalRequest struct {
	// AttemptDir is the retained local Run's factory workspace, as a path
	// relative to the repository, for example .nvkr/runs/<attempt>.
	AttemptDir string

	PlannersDir string
	Vars        map[string]map[string]string
}

// localCapture is what GenerateLocal needs from a captured local Run. The
// lifecycle owner satisfies it.
type localCapture interface {
	Description() (lifecycle.LocalDiagnosticDescription, error)
	Close() error
}

type captureLocalFunc func(context.Context, string) (localCapture, error)

// GenerateLocal plans for a complete local Run instead of a published release.
// The Run is checked by the existing local owner, which verifies its Plan, its
// compiler inputs, its complete output membership and its held payloads, and
// signs and publishes nothing. A local Run carries no registry location, so the
// plan names each output as local/<attempt>/<output>@<digest>, which no executor
// can fetch. It exists to exercise planning without a registry or credentials.
func GenerateLocal(ctx context.Context, req LocalRequest, planners map[string]Planner) (*Plan, error) {
	return generateLocal(ctx, req, planners, captureLocal)
}

func captureLocal(ctx context.Context, attemptDir string) (localCapture, error) {
	return lifecycle.CaptureLocalDiagnosticArtifacts(ctx, attemptDir)
}

func generateLocal(ctx context.Context, req LocalRequest, planners map[string]Planner, capture captureLocalFunc) (plan *Plan, err error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "validation planning requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation planning canceled")
	}
	for name, value := range map[string]string{"local Run directory": req.AttemptDir, "planners directory": req.PlannersDir} {
		if strings.TrimSpace(value) == "" {
			return nil, e.New(e.CodeUsage, "validation planning requires a %s", name)
		}
	}
	// The planners are admitted before the Run is captured, so a bad directory
	// fails without reading the Run's payloads.
	if _, err := LoadConfig(req.PlannersDir); err != nil {
		return nil, err
	}

	held, err := capture(ctx, req.AttemptDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := held.Close(); closeErr != nil {
			plan = nil
			err = stderrors.Join(err, e.Wrap(e.CodeGateUnmet, closeErr, "closing the local Run"))
		}
	}()
	description, err := held.Description()
	if err != nil {
		return nil, err
	}
	release, err := releaseFromLocal(description)
	if err != nil {
		return nil, err
	}
	return Build(ctx, Request{Release: release, PlannersDir: req.PlannersDir, Vars: req.Vars}, planners)
}

// releaseFromLocal reads the release a planner sees out of a captured local Run.
func releaseFromLocal(description lifecycle.LocalDiagnosticDescription) (Release, error) {
	if description.Attempt == "" || !canonical.IsDigest(description.ContentSubject) {
		return Release{}, e.New(e.CodeProvenanceMissing, "the local Run names no attempt or content subject")
	}
	base := LocalPrefix + description.Attempt
	outputs := make([]Output, 0, len(description.Outputs))
	for _, o := range description.Outputs {
		if o.Name == "" || !canonical.IsDigest(o.Digest) {
			return Release{}, e.New(e.CodeProvenanceMissing, "local output %q lacks a name or a digest", o.Name)
		}
		platform := ""
		if o.OS != "" && o.Arch != "" {
			platform = o.OS + "/" + o.Arch
		}
		outputs = append(outputs, Output{
			Name: o.Name, Kind: o.Kind, Platform: platform, Digest: o.Digest,
			Reference: base + "/" + o.Name + "@" + o.Digest,
			// A local output has no source tag, so its identity is its digest.
			Version: o.Digest,
		})
	}
	return Release{Reference: base + "@" + description.ContentSubject, Outputs: outputs}, nil
}
