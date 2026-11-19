// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// completedRun is what binding needs from one captured, completed Run.
type completedRun interface {
	Description() (lifecycle.LocalDiagnosticDescription, error)
	Producer(output string) (*factory.CLIArtifactTestInputs, error)
	Close() error
}

// localRun adapts the lifecycle owner, which requires a complete local Run and
// checks every output's bytes before issuing it.
type localRun struct {
	*lifecycle.LocalDiagnosticArtifacts
}

func openLocalRun(ctx context.Context, attempt string) (completedRun, error) {
	owner, err := lifecycle.CaptureLocalDiagnosticArtifacts(ctx, attempt)
	if err != nil {
		return nil, err
	}
	return localRun{owner}, nil
}

func (r localRun) Producer(output string) (*factory.CLIArtifactTestInputs, error) {
	plan, err := r.Plan()
	if err != nil {
		return nil, err
	}
	return factory.SelectCLIArtifactTestInputs(plan, output)
}

// BoundValidation holds the completed Run open and records each suite's exact
// input bindings, test origin and image. Close releases the Run.
type BoundValidation struct {
	run      completedRun
	identity RunIdentity
	suites   []BoundSuite
	once     sync.Once
	closeErr error
}

// RunIdentity names the completed Run that was bound.
type RunIdentity struct {
	Attempt    string
	PlanDigest string
}

// BoundSuite is a prepared suite resolved against the completed Run.
type BoundSuite struct {
	PreparedSuite
	Inputs     map[string]BoundInput
	TestOrigin TestOrigin
	Image      string
}

// BoundInput is one input role bound to an exact named Run output.
type BoundInput struct {
	Output      string
	Component   string
	Format      string
	Platform    string
	Digest      string
	BuildDigest string
	Version     string
}

// TestOrigin is the exact source the suite's tests come from.
type TestOrigin struct {
	Repository string
	Commit     string
	Tree       string
}

// Bind opens the completed Run at attempt and binds every prepared suite to it.
func (p *PreparedValidation) Bind(ctx context.Context, attempt string) (*BoundValidation, error) {
	return p.bind(ctx, attempt, openLocalRun)
}

func (p *PreparedValidation) bind(ctx context.Context, attempt string, open func(context.Context, string) (completedRun, error)) (*BoundValidation, error) {
	if p == nil || len(p.suites) == 0 {
		return nil, usage("binding requires a prepared validation")
	}
	if ctx == nil {
		return nil, usage("binding requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation canceled before binding")
	}
	if !pathText(attempt) {
		return nil, usage("binding requires a Run attempt directory")
	}
	run, err := open(ctx, attempt)
	if err != nil {
		return nil, err
	}
	bound, err := bindRun(p, run)
	if err != nil {
		return nil, errors.Join(err, run.Close())
	}
	return bound, nil
}

func bindRun(p *PreparedValidation, run completedRun) (*BoundValidation, error) {
	description, err := run.Description()
	if err != nil {
		return nil, err
	}
	producers := map[string]*factory.CLIArtifactTestInputs{}
	bound := &BoundValidation{run: run, identity: RunIdentity{Attempt: description.Attempt, PlanDigest: description.PlanDigest}}
	for _, suite := range p.suites {
		result := BoundSuite{PreparedSuite: suite, Inputs: map[string]BoundInput{}}
		for _, role := range slices.Sorted(maps.Keys(suite.Inputs)) {
			input, producer, err := bindInput(description, suite.Requires.Platforms, suite.Inputs[role], run, producers)
			if err != nil {
				return nil, keepCode(err, "suite %q input %q", suite.ID, role)
			}
			result.Inputs[role] = input
			if role == suite.TestOrigin.FromInput {
				result.TestOrigin = TestOrigin{Repository: producer.Repo, Commit: producer.Commit, Tree: producer.TreeHash}
			}
			if role == suite.Image.FromInput {
				result.Image = producer.ToolchainImage
			}
		}
		bound.suites = append(bound.suites, result)
	}
	return bound, nil
}

// bindInput requires exactly one named output of the declared format and
// platform, built from exactly one subject of the declared component, and a
// producer description that agrees with it.
func bindInput(description lifecycle.LocalDiagnosticDescription, platforms []string, input RegistrationInput, run completedRun,
	producers map[string]*factory.CLIArtifactTestInputs) (BoundInput, *factory.CLIArtifactTestInputs, error) {
	matches := 0
	var output int
	for i := range description.Outputs {
		if description.Outputs[i].Name == input.Output {
			matches++
			output = i
		}
	}
	if matches != 1 {
		return BoundInput{}, nil, e.New(e.CodeVerifyFailed, "Run must contain exactly one output named %q", input.Output)
	}
	selected := description.Outputs[output]
	platform := selected.OS + "/" + selected.Arch
	if selected.Kind != input.Format {
		return BoundInput{}, nil, e.New(e.CodeEngineCapability, "output %q is %q, not %q", input.Output, selected.Kind, input.Format)
	}
	if !slices.Contains(platforms, platform) {
		return BoundInput{}, nil, e.New(e.CodeEngineCapability, "output %q platform %q is not a declared platform", input.Output, platform)
	}
	var digest string
	subjects := 0
	for _, subject := range selected.BuildSubjects {
		if subject.Component == input.Component {
			subjects++
			digest = subject.Digest
		}
	}
	if subjects != 1 {
		return BoundInput{}, nil, e.New(e.CodeVerifyFailed, "output %q must have exactly one %q build subject", input.Output, input.Component)
	}
	producer, ok := producers[input.Output]
	if !ok {
		var err error
		if producer, err = run.Producer(input.Output); err != nil {
			return BoundInput{}, nil, err
		}
		producers[input.Output] = producer
	}
	if producer.Output != input.Output || producer.BuildDigest != selected.BuildDigest || producer.Platform != platform {
		return BoundInput{}, nil, e.New(e.CodeVerifyFailed, "output %q differs from its Plan producer", input.Output)
	}
	return BoundInput{Output: input.Output, Component: input.Component, Format: input.Format, Platform: platform,
		Digest: digest, BuildDigest: selected.BuildDigest, Version: producer.ExpectedVersion}, producer, nil
}

// Run returns the bound Run's identity.
func (b *BoundValidation) Run() RunIdentity { return b.identity }

// Suites returns detached bound suites in selection order.
func (b *BoundValidation) Suites() []BoundSuite {
	result := make([]BoundSuite, 0, len(b.suites))
	for _, suite := range b.suites {
		suite.Registration = suite.detached()
		suite.Inputs = maps.Clone(suite.Inputs)
		result = append(result, suite)
	}
	return result
}

// Close releases the completed Run; repeated calls return the first result.
func (b *BoundValidation) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() { b.closeErr = b.run.Close() })
	return b.closeErr
}

// keepCode adds context while preserving the cause's code.
func keepCode(err error, format string, args ...any) error {
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(err, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, err, format, args...)
}
