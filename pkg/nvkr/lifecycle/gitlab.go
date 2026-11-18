// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// FinalizeGitLab observes the exact restored output closure only after the
// native provider owner admits the current jobs and their original carriers.
// Job is a description, not a receipt. The result remains unsigned and
// static-unqualified; GitHub signing and qualification are not substitutes.
func FinalizeGitLab(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, workspace, outputPath string, job factory.GitLabJobContext) (*Run, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "GitLab Run finalization requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "GitLab Run finalization canceled")
	}
	attempt, err := factory.GitLabAttempt(job)
	if err != nil {
		return nil, err
	}
	if workspace == "" || outputPath == "" {
		return nil, e.New(e.CodeUsage, "GitLab Run finalization requires a workspace and output")
	}
	if filepath.Clean(workspace) != filepath.Join(".nvkr", "factory") {
		return nil, e.New(e.CodeEngineCapability, "GitLab Run finalization requires the compiled workspace")
	}
	if _, err := LoadDefinitionInput(ctx, definitionPath); err != nil {
		return nil, err
	}
	if _, err := LoadEnvironment(ctx, environmentPath); err != nil {
		return nil, err
	}
	plan, err := factory.LoadPlanContext(ctx, planPath)
	if err != nil {
		return nil, err
	}
	if definitionPath != plan.DefinitionPath || environmentPath != plan.EnvironmentPath {
		return nil, e.New(e.CodePipelineDrift, "GitLab authored input paths differ from the exact generated Plan")
	}
	// Admit the provider's supported execution slice before compilation can
	// acquire source. Check the complete committed generated closure as well.
	if err := factory.ValidateGitLabPlan(ctx, plan); err != nil {
		return nil, err
	}
	if err := factory.CheckGenerated(ctx, plan, factory.EngineGitLab, "."); err != nil {
		return nil, err
	}
	// The declared IDs and commit are not provider authority. Authenticate the
	// exact native finalizer role before compilation can fetch product source,
	// and retain that opaque owner for the later carrier/outcome admission.
	finalizer, err := factory.AdmitGitLabFinalizer(ctx, plan, job)
	if err != nil {
		return nil, err
	}
	defer func() { _ = finalizer.Close() }()
	expectedPlan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return nil, err
	}
	want, err := expectedPlan.Digest()
	if err != nil {
		return nil, err
	}
	got, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, e.New(e.CodePipelineDrift, "GitLab Run Plan differs from the exact authored input selection")
	}
	run, err := PrepareRun(expectedPlan.Definition, expectedPlan.Environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	// Failed or skipped provider jobs may leave no restored artifact directory.
	// Prepare the confined output parent before failure accounting, through the
	// existing no-follow owner; the immutable Run writer still refuses replacement.
	parent, _, err := securepath.CreateParentBeneath(".", outputPath)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "preparing GitLab Run output parent")
	}
	if err := parent.Close(); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "closing GitLab Run output parent")
	}
	if err := factory.ValidateGitLabOutcomes(ctx, workspace, plan, finalizer); err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "recording failed GitLab Run")
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	observed, err := observeHostedOutputs(ctx, workspace, run.Expected, artifacts, plan)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "observing exact GitLab output closure")
	}
	bound, err := bindObserved(run, observed)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "binding exact GitLab output closure")
	}
	bound, err = bindReleaseContent(plan, bound)
	if err != nil {
		return recordRunFailure(ctx, outputPath, run, err, "binding GitLab release content")
	}
	if err := writeRunRecord(ctx, outputPath, bound); err != nil {
		return nil, err
	}
	return bound, nil
}
