// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// BuildRequest names the authored inputs of a new local Run. Definition and
// Environment are relative to the working directory, which must be the
// repository root: compilation captures their exact bytes so the completed Run
// can be bound.
type BuildRequest struct {
	Definition       string
	Environment      string
	Attempt          string
	AttemptsRoot     string // empty uses lifecycle's default attempts root
	GeneratorVersion string
	Stdout, Stderr   io.Writer
}

// plannedBuild is a compiled local Run that has not executed yet.
type plannedBuild interface {
	Producer(output, component string) (*factory.CLIArtifactTestInputs, error)
	AdmitCapture() error
	Execute(ctx context.Context) (builtRun, error)
}

// builtRun is the completed attempt a build produced and where lifecycle kept it.
type builtRun struct {
	directory string
	identity  RunIdentity
}

type localBuild struct {
	prepared *lifecycle.PreparedRun
	request  BuildRequest
}

func planLocalBuild(ctx context.Context, request BuildRequest) (plannedBuild, error) {
	prepared, err := lifecycle.PrepareCapturedFiles(ctx, ".", request.Definition, request.Environment, request.GeneratorVersion, request.Attempt)
	if err != nil {
		return nil, err
	}
	return localBuild{prepared: prepared, request: request}, nil
}

func (b localBuild) Producer(output, component string) (*factory.CLIArtifactTestInputs, error) {
	return b.prepared.CLIArtifactTestInputs(output, component)
}

func (b localBuild) AdmitCapture() error { return b.prepared.AdmitLocalDiagnosticCapture() }

func (b localBuild) Execute(ctx context.Context) (builtRun, error) {
	run, err := lifecycle.ExecuteLocal(ctx, b.prepared, lifecycle.LocalOptions{AttemptsRoot: b.request.AttemptsRoot,
		Stdout: b.request.Stdout, Stderr: b.request.Stderr})
	if err != nil {
		return builtRun{}, err
	}
	if run == nil || run.Status != lifecycle.RunComplete {
		return builtRun{}, e.New(e.CodeGateUnmet, "local build did not complete")
	}
	return builtRun{directory: lifecycle.LocalAttemptDirectory(b.request.AttemptsRoot, run.Attempt),
		identity: RunIdentity{Attempt: run.Attempt, PlanDigest: run.PlanDigest, ContentSubject: run.ContentSubject}}, nil
}

// Build compiles the request, checks that the Run can be bound and that every
// selected suite's inputs are planned outputs of the declared component on a
// declared platform, then builds a new local Run and binds it exactly as Bind
// binds an existing Run. A failed build binds nothing. A completed attempt is
// kept even when binding refuses it; the error names its directory so it can
// be bound later with Bind.
func (p *PreparedValidation) Build(ctx context.Context, request BuildRequest) (*BoundValidation, error) {
	return p.build(ctx, request, planLocalBuild, openLocalRun)
}

func (p *PreparedValidation) build(ctx context.Context, request BuildRequest, plan func(context.Context, BuildRequest) (plannedBuild, error),
	open func(context.Context, string) (completedRun, error)) (*BoundValidation, error) {
	if p == nil || len(p.suites) == 0 {
		return nil, usage("building requires a prepared validation")
	}
	if ctx == nil {
		return nil, usage("building requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation canceled before building")
	}
	if !pathText(request.Definition) || !pathText(request.Environment) || !pathText(request.Attempt) || request.GeneratorVersion == "" ||
		(request.AttemptsRoot != "" && !pathText(request.AttemptsRoot)) {
		return nil, usage("building requires a Definition, Environment, attempt and generator version")
	}
	planned, err := plan(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := planned.AdmitCapture(); err != nil {
		return nil, err
	}
	for _, suite := range p.suites {
		for _, role := range slices.Sorted(maps.Keys(suite.Inputs)) {
			input := suite.Inputs[role]
			producer, err := planned.Producer(input.Output, input.Component)
			if err != nil {
				return nil, keepCode(err, "suite %q input %q", suite.ID, role)
			}
			if producer.Output != input.Output {
				return nil, e.New(e.CodeVerifyFailed, "suite %q input %q: planned output differs from %q", suite.ID, role, input.Output)
			}
			if !slices.Contains(suite.Requires.Platforms, producer.Platform) {
				return nil, e.New(e.CodeEngineCapability, "suite %q input %q: planned platform %q is not a declared platform", suite.ID, role, producer.Platform)
			}
		}
	}
	built, err := planned.Execute(ctx)
	if err != nil {
		return nil, err
	}
	bound, err := p.bind(ctx, built.directory, open)
	if err != nil {
		return nil, keepCode(err, "built Run %q was kept; bind it later", built.directory)
	}
	if bound.Run() != built.identity {
		err := e.New(e.CodeVerifyFailed, "Run bound at %q is not the Run this build completed", built.directory)
		return nil, errors.Join(err, bound.Close())
	}
	return bound, nil
}
