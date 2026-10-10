// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

const (
	RunPending  = "pending"
	RunFailed   = "failed"
	RunComplete = "complete"
)

// PrepareRun freezes required output slots before execution. The returned Run
// is descriptive state; it is not a resumable execution or release capability.
func PrepareRun(definition Definition, environment Environment, plan *factory.Plan, attempt string) (*Run, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "run preparation requires a compiled plan")
	}
	if !namePattern.MatchString(attempt) {
		return nil, e.New(e.CodeUsage, "run preparation requires a canonical attempt name")
	}
	definitionDigest, err := definition.Digest()
	if err != nil {
		return nil, err
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		return nil, err
	}
	if plan.DefinitionDigest != definitionDigest || plan.EnvironmentDigest != environmentDigest {
		return nil, e.New(e.CodeUnverifiedRef, "compiled plan does not bind the supplied Definition and Environment")
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	run := &Run{SchemaVersion: RunSchemaVersion, Attempt: attempt, DefinitionDigest: definitionDigest,
		EnvironmentDigest: environmentDigest, PlanDigest: planDigest,
		Lane: LaneUnqualified, Status: RunPending}
	outputs := make(map[string]factory.LifecycleOutputPlan, len(plan.LifecycleOutputs))
	for _, output := range plan.LifecycleOutputs {
		outputs[output.Name] = output
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, output := range definition.Outputs {

		compiledOutput, ok := outputs[output.Name]
		if !ok || compiledOutput.Kind != output.Format || !equalStrings(compiledOutput.Components, output.Components) {
			return nil, e.New(e.CodeUnverifiedRef, "run plan omits exact compiled lifecycle output %q", output.Name)
		}
		artifactID := compiledOutput.ArtifactID
		memberArtifactID := compiledOutput.ArtifactID
		if output.Format == OutputExecutable || output.Format == OutputOCI || output.Format == OutputDiskImage {
			if environment.Publication != nil {
				artifactID = "package/" + publicationPackageName
			}
		}

		slot := requiredOutput{Name: output.Name, Kind: output.Format, ArtifactID: artifactID, MemberArtifactID: memberArtifactID,
			BuildDigest: compiledOutput.BuildDigest, Components: append([]string(nil), output.Components...), ImageIndex: compiledOutput.ImageIndex.Clone()}
		bindings, err := requiredProducerBindings(slot)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			result, exists := artifacts[binding.id]
			if !exists || result.Kind != "producer-result" || result.Component != binding.component || result.BuildTarget != binding.target {
				return nil, e.New(e.CodeUnverifiedRef, "run plan omits exact producer result %q for output %q", binding.id, output.Name)
			}
			slot.ComponentResults = append(slot.ComponentResults, binding.id)
		}
		run.Expected = append(run.Expected, slot)
	}
	sort.Slice(run.Expected, func(i, j int) bool { return run.Expected[i].Name < run.Expected[j].Name })
	return run, nil
}

// bindObserved returns a new descriptive Run after checking exact observations
// against the frozen slots. Observations never mutate or shrink requirements.
func bindObserved(run *Run, artifacts []Artifact) (*Run, error) {
	if err := validateRun(run); err != nil {
		return nil, err
	}
	if run.Status != RunPending || len(run.Observed) != 0 {
		return nil, e.New(e.CodeImmutableViolation, "run attempt has already recorded observations; create an explicit new attempt")
	}
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = nil
	required := make(map[string]requiredOutput, len(run.Expected))
	for _, slot := range run.Expected {
		required[slot.Name] = slot
	}
	seen := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return nil, err
		}
		slot, ok := required[artifact.Name]
		if !ok {
			return nil, e.New(e.CodeReleaseIncomplete, "run observed unexpected artifact %q", artifact.Name)
		}
		if seen[artifact.Name] {
			return nil, e.New(e.CodeReleaseIncomplete, "run observed artifact %q more than once", artifact.Name)
		}
		if artifact.Kind != slot.Kind || artifact.BuildDigest != slot.BuildDigest || !equalStrings(artifact.Components, slot.Components) {
			return nil, e.New(e.CodeVerifyFailed, "run artifact %q does not satisfy its frozen kind/build/component slot", artifact.Name)
		}
		if err := factory.ValidateLifecycleOutputs([]factory.LifecycleOutputPlan{{Name: slot.Name, Kind: slot.Kind, BuildDigest: slot.BuildDigest, Components: slot.Components, ImageIndex: slot.ImageIndex}}, []factory.LifecycleBoundOutput{artifact}); err != nil {
			return nil, err
		}
		if artifact.Kind == OutputDiskImage && (artifact.Disk == nil || artifact.Disk.PlanDigest != run.PlanDigest) {
			return nil, e.New(e.CodeVerifyFailed,
				"run disk artifact %q does not bind its frozen Plan identity", artifact.Name)
		}
		seen[artifact.Name] = true
		copyRun.Observed = append(copyRun.Observed, cloneArtifact(artifact))
	}
	sort.Slice(copyRun.Observed, func(i, j int) bool { return copyRun.Observed[i].Name < copyRun.Observed[j].Name })
	if len(run.diskInventories) != 0 {
		if err := validateInstalledInventorySubjects(&copyRun); err != nil {
			return nil, err
		}
	}
	if len(seen) == len(required) {
		copyRun.Status = RunComplete
	} else {
		copyRun.Status = RunFailed
		missing := e.New(e.CodeReleaseIncomplete, "run attempt did not observe every frozen output")
		copyRun.Failure = &runFailure{Code: string(e.CodeReleaseIncomplete), Message: missing.Error()}
	}
	return &copyRun, nil
}

func validateRun(run *Run) error {
	if run == nil || run.SchemaVersion != RunSchemaVersion || !namePattern.MatchString(run.Attempt) || !canonical.IsDigest(run.DefinitionDigest) ||
		!canonical.IsDigest(run.EnvironmentDigest) || !canonical.IsDigest(run.PlanDigest) || len(run.Expected) == 0 {
		return e.New(e.CodeVerifyFailed, "run does not contain a complete frozen input and output description")
	}
	switch run.Status {
	case RunPending:
		if run.Failure != nil || len(run.Observed) != 0 {
			return e.New(e.CodeVerifyFailed, "pending run contains terminal state")
		}
	case RunFailed:
		if run.Failure == nil || run.Failure.Code == "" || run.Failure.Message == "" {
			return e.New(e.CodeVerifyFailed, "failed run omits its coded failure")
		}
	case RunComplete:
		if run.Failure != nil || len(run.Observed) != len(run.Expected) {
			return e.New(e.CodeVerifyFailed, "complete run does not contain exact observations")
		}
	default:
		return e.New(e.CodeVerifyFailed, "run status %q is unsupported", run.Status)
	}
	previous := ""
	for _, slot := range run.Expected {
		if !namePattern.MatchString(slot.Name) || slot.Name <= previous || slot.ArtifactID == "" || slot.MemberArtifactID == "" || !canonical.IsDigest(slot.BuildDigest) ||
			(slot.Kind != OutputHelm && len(slot.Components) == 0) {
			return e.New(e.CodeVerifyFailed, "run required outputs must be canonical, sorted, unique, and complete")
		}
		bindings, err := requiredProducerBindings(slot)
		if err != nil {
			return err
		}
		if len(bindings) != len(slot.ComponentResults) {
			return e.New(e.CodeVerifyFailed, "run omits frozen native producer results")
		}
		for i, binding := range bindings {
			if slot.ComponentResults[i] != binding.id {
				return e.New(e.CodeVerifyFailed, "run substitutes frozen native producer result")
			}
		}
		previous = slot.Name
	}
	return nil
}

func failedRun(run *Run, cause error) *Run {
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = nil
	copyRun.diskInventories = nil
	copyRun.Status = RunFailed
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(cause, &coded) {
		code = coded.Code
	}
	copyRun.Failure = &runFailure{Code: string(code), Message: cause.Error()}
	return &copyRun
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRequired(source []requiredOutput) []requiredOutput {
	clone := make([]requiredOutput, len(source))
	for index, slot := range source {
		clone[index] = slot
		clone[index].ImageIndex = slot.ImageIndex.Clone()
		clone[index].Components = append([]string(nil), slot.Components...)
		clone[index].ComponentResults = append([]string(nil), slot.ComponentResults...)
	}
	return clone
}
