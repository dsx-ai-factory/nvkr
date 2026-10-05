package lifecycle

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

// FinalizeGenerated re-admits the authored inputs and committed plan, then
// observes artifacts restored by a hosted pipeline into one immutable Run.
func FinalizeGenerated(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath string) (*Run, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	expectedPlan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	plan, err := factory.LoadPlanContext(ctx, planPath)
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
	if want != got {
		return nil, e.New(e.CodePipelineDrift, "hosted Run plan %s differs from recompiled plan %s", got, want)
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
	if err := writeRunRecord(ctx, outputPath, bound); err != nil {
		return nil, err
	}
	return bound, nil
}
