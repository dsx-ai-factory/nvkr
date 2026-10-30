// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

// FinalizeGenerated re-admits the authored inputs and committed plan, then
// observes artifacts restored by a hosted pipeline into one immutable Run.
func FinalizeGenerated(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath string) (*Run, error) {
	return finalizeGenerated(ctx, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath, "")
}

func finalizeGenerated(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath, handoffPath string) (*Run, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "hosted Run finalization requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "hosted Run finalization canceled")
	}
	var source qualificationorigin.Description
	if handoffPath != "" {
		var err error
		source, err = producingSourceDescription(attempt, os.Getenv)
		if err != nil {
			return nil, err
		}
	}
	if workspace == "" {
		return nil, e.New(e.CodeUsage, "hosted Run finalization requires a workspace")
	}
	// Preserve authored-input admission before reading a generated Plan. These
	// bounded document reads do not compile catalogs or acquire source trees.
	if _, err := LoadDefinition(ctx, definitionPath); err != nil {
		return nil, err
	}
	if _, err := LoadEnvironment(ctx, environmentPath); err != nil {
		return nil, err
	}
	var expectedPlan *factory.Plan
	var inputs factory.InputSnapshot
	var err error
	plan, err := factory.LoadPlanContext(ctx, planPath)
	if err != nil {
		return nil, err
	}
	if plan.Environment.GitHubRuntimeSource != nil && plan.RuntimeSourceCommit == "" {
		return nil, e.New(e.CodeUnpinnedSource, "hosted source-policy Run lacks its frozen source commit")
	}
	if plan.RuntimeSourceCommit != "" && handoffPath != "" {
		return nil, e.New(e.CodeEngineCapability, "runtime source selection has no qualification input-capture replay route")
	}
	if len(plan.SourceOverrides) != 0 && handoffPath != "" {
		return nil, e.New(e.CodeEngineCapability, "source overrides have no qualification input-capture replay route")
	}
	if plan.RuntimeSourceCommit != "" {
		expectedPlan, err = CompileRuntimeSourceFiles(ctx, definitionPath, environmentPath, generatorVersion, plan.RuntimeSourceCommit)
	} else if len(plan.SourceOverrides) != 0 {
		expectedPlan, err = ReplayDevelopmentSourceOverrideFiles(ctx, definitionPath, environmentPath, generatorVersion, plan.SourceOverrides)
	} else if handoffPath != "" {
		expectedPlan, inputs, err = factory.CaptureCompileFiles(ctx, ".", definitionPath, environmentPath, generatorVersion)
	} else {
		expectedPlan, err = CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	}
	if err != nil {
		return nil, err
	}
	definition, environment := expectedPlan.Definition, expectedPlan.Environment
	want, err := expectedPlan.Digest()
	if err != nil {
		return nil, err
	}
	got, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if want != got {
		return nil, e.New(e.CodePipelineDrift, "hosted Run plan %s differs from recompiled plan %s", got, want)
	}
	if handoffPath != "" {
		if err := admitQualificationProducer(plan, source); err != nil {
			return nil, err
		}
	}
	run, err := PrepareRun(definition, environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	if restoreErr := factory.ValidateHostedRestores(ctx, workspace, plan); restoreErr != nil {
		return recordRunFailure(ctx, outputPath, run, restoreErr, "recording hosted failed Run")
	}
	if factory.PerOutputPublication(plan) {
		observed, err := factory.AuthenticateHostedReferenceRelease(ctx, workspace, plan)
		if err != nil {
			return recordRunFailure(ctx, outputPath, run, err, "authenticating hosted reference release")
		}
		run.Lane = LanePipeline
		bound, err := bindObserved(run, observed)
		if err != nil {
			return recordRunFailure(ctx, outputPath, run, err, "binding hosted reference release")
		}
		bound, err = bindReleaseContent(plan, bound)
		if err != nil {
			return recordRunFailure(ctx, outputPath, run, err, "binding reference release content")
		}
		if handoffPath != "" {
			if err := writeQualificationHandoff(ctx, handoffPath, workspace, plan, inputs, source); err != nil {
				return recordRunFailure(ctx, outputPath, run, err, "recording qualification handoff failure")
			}
		}
		if err := writeRunRecord(ctx, outputPath, bound); err != nil {
			return nil, err
		}
		return bound, nil
	}
	observed, err := observeHostedOutputs(ctx, workspace, run.Expected, artifacts, plan)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "recording hosted failed Run")
	}
	if observeErr := factory.ValidateHostedOutcomes(ctx, workspace, plan); observeErr != nil {
		return recordRunFailure(ctx, outputPath, run, observeErr, "recording hosted failed Run")
	}
	if plan.Qualification.String() == LanePipeline {
		if observeErr := observeHostedPipelineEvidence(ctx, workspace, plan, artifacts, observed); observeErr != nil {
			return recordRunFailure(ctx, outputPath, run, observeErr, "recording hosted failed Run")
		}
		run.Lane = LanePipeline
	}
	bound, err := bindObserved(run, observed)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "recording hosted failed Run")
	}
	bound, err = bindReleaseContent(plan, bound)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "recording hosted failed Run")
	}
	if handoffPath != "" {
		if err := writeQualificationHandoff(ctx, handoffPath, workspace, plan, inputs, source); err != nil {
			return recordRunFailure(ctx, outputPath, run, err, "recording qualification handoff failure")
		}
	}
	if err := writeRunRecord(ctx, outputPath, bound); err != nil {
		return nil, err
	}
	return bound, nil
}
