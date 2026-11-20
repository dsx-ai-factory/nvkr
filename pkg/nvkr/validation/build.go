// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"io"
	"maps"
	"path/filepath"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// BuildRequest names the authored inputs of a new local Run. Definition and
// Environment are repository-relative: compilation captures their exact bytes
// beneath the working directory so the completed Run can be bound.
type BuildRequest struct {
	Definition       string
	Environment      string
	Attempt          string
	AttemptsRoot     string // empty uses lifecycle.DefaultAttemptsRoot
	GeneratorVersion string
	Stdout, Stderr   io.Writer
}

// plannedBuild is a compiled local Run that has not executed yet.
type plannedBuild interface {
	Producer(output string) (*factory.CLIArtifactTestInputs, error)
	Execute(ctx context.Context) (attemptDirectory string, err error)
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

func (b localBuild) Producer(output string) (*factory.CLIArtifactTestInputs, error) {
	return b.prepared.CLIArtifactTestInputs(output)
}

func (b localBuild) Execute(ctx context.Context) (string, error) {
	run, err := lifecycle.ExecuteLocal(ctx, b.prepared, lifecycle.LocalOptions{AttemptsRoot: b.request.AttemptsRoot,
		Stdout: b.request.Stdout, Stderr: b.request.Stderr})
	if err != nil {
		return "", err
	}
	if run == nil || run.Status != lifecycle.RunComplete {
		return "", e.New(e.CodeGateUnmet, "local build did not complete")
	}
	root := b.request.AttemptsRoot
	if root == "" {
		root = lifecycle.DefaultAttemptsRoot
	}
	return filepath.ToSlash(filepath.Join(root, run.Attempt)), nil
}

// Build compiles the request, checks that every selected suite's inputs are
// planned outputs on a declared platform, then builds a new local Run and binds
// it exactly as Bind binds an existing Run. A failed build binds nothing.
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
	for _, suite := range p.suites {
		for _, role := range slices.Sorted(maps.Keys(suite.Inputs)) {
			output := suite.Inputs[role].Output
			producer, err := planned.Producer(output)
			if err != nil {
				return nil, keepCode(err, "suite %q input %q", suite.ID, role)
			}
			if producer.Output != output {
				return nil, e.New(e.CodeVerifyFailed, "suite %q input %q: planned output differs from %q", suite.ID, role, output)
			}
			if !slices.Contains(suite.Requires.Platforms, producer.Platform) {
				return nil, e.New(e.CodeEngineCapability, "suite %q input %q: planned platform %q is not a declared platform", suite.ID, role, producer.Platform)
			}
		}
	}
	attempt, err := planned.Execute(ctx)
	if err != nil {
		return nil, err
	}
	return p.bind(ctx, attempt, open)
}
