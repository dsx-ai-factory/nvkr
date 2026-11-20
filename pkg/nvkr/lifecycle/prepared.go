// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// PreparedRun is the opaque prepared lifecycle input. Callers can inspect Run;
// the generated compiler plan remains owned by this module.
type PreparedRun struct {
	// Set only by the owned built-in policy constructor; never decoded from input.
	defaultLocal bool
	plan         *factory.Plan
	run          *Run
	// Captured compiler reads are immutable transport, not signing authority.
	inputs string
}

// PrepareCapturedFiles prepares the same lifecycle Run while retaining the exact
// authored bytes read by compilation. Paths are logical repository-relative
// paths beneath root. Local execution saves this snapshot before producer work;
// qualification must still authenticate the Run and every output independently.
func PrepareCapturedFiles(ctx context.Context, root, definitionPath, environmentPath, generatorVersion, attempt string) (*PreparedRun, error) {
	plan, inputs, err := factory.CaptureCompileFiles(ctx, root, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return nil, err
	}
	return prepareCaptured(plan, inputs, attempt)
}

func prepareCaptured(plan *factory.Plan, inputs factory.InputSnapshot, attempt string) (*PreparedRun, error) {
	run, err := PrepareRun(plan.Definition, plan.Environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	return &PreparedRun{plan: plan, run: run, inputs: string(raw)}, nil
}

// PrepareDefaultLocal prepares a build-only Run with the built-in local policy.
// Definition paths are relative to root, as with PrepareCapturedFiles.
func PrepareDefaultLocal(ctx context.Context, root, definitionPath, generatorVersion, attempt string) (*PreparedRun, error) {
	plan, inputs, err := factory.CaptureCompileDefaultLocal(ctx, root, definitionPath, generatorVersion)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareCaptured(plan, inputs, attempt)
	if err != nil {
		return nil, err
	}
	prepared.defaultLocal = true
	return prepared, nil
}

func PrepareFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, attempt string) (*PreparedRun, error) {
	// A composite prepares through bounded capture beneath the working
	// directory, so its static-unqualified local Run retains member bytes.
	if kind, err := authored.ReadDocumentKind(ctx, definitionPath); err == nil && kind == authored.CompositeKind {
		relativeDefinition, err := authored.WorkingRelativePath(definitionPath)
		if err != nil {
			return nil, err
		}
		relativeEnvironment, err := authored.WorkingRelativePath(environmentPath)
		if err != nil {
			return nil, err
		}
		return PrepareCapturedFiles(ctx, ".", relativeDefinition, relativeEnvironment, generatorVersion, attempt)
	}
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	run, err := PrepareRun(definition, environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	return &PreparedRun{plan: plan, run: run}, nil
}

// CLIArtifactTestInputs derives one explicitly named CLI output's test inputs
// from the compiled Plan before any build. The Plan itself stays owned here.
func (prepared *PreparedRun) CLIArtifactTestInputs(output, component string) (*factory.CLIArtifactTestInputs, error) {
	if prepared == nil || prepared.plan == nil {
		return nil, e.New(e.CodeUsage, "CLI test inputs require a prepared Run")
	}
	return factory.SelectCLIArtifactTestInputs(prepared.plan, output, component)
}

// AdmitLocalDiagnosticCapture refuses, before any build, a Plan whose completed
// Run CaptureLocalDiagnosticArtifacts would refuse.
func (prepared *PreparedRun) AdmitLocalDiagnosticCapture() error {
	if prepared == nil || prepared.plan == nil {
		return e.New(e.CodeUsage, "diagnostic capture admission requires a prepared Run")
	}
	return admitLocalDiagnosticPlan(prepared.plan)
}

func (prepared *PreparedRun) Description() Run {
	if prepared == nil || prepared.run == nil {
		return Run{}
	}
	copyRun := *prepared.run
	copyRun.Expected = cloneRequired(prepared.run.Expected)
	copyRun.Composition = prepared.run.Composition.Clone()
	copyRun.Observed = cloneArtifacts(prepared.run.Observed)
	if prepared.run.Failure != nil {
		failure := *prepared.run.Failure
		copyRun.Failure = &failure
	}
	return copyRun
}

func cloneArtifacts(source []Artifact) []Artifact {
	clone := make([]Artifact, len(source))
	for index, artifact := range source {
		clone[index] = cloneArtifact(artifact)
	}
	return clone
}

func cloneArtifact(artifact Artifact) Artifact {
	clone := artifact
	clone.Derived = artifact.Derived.Clone()
	clone.ImageIndex = artifact.ImageIndex.Clone()
	clone.Components = append([]string(nil), artifact.Components...)
	if artifact.Disk != nil {
		disk := *artifact.Disk
		clone.Disk = &disk
	}
	return clone
}
