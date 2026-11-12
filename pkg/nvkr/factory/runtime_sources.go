// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CheckFrozenAuthoredInputs binds the restored snapshot to the trusted workflow
// checkout without consulting any mutable source ref. Complete replay separately
// proves that these are exactly the files the compiler consumed.
func CheckFrozenAuthoredInputs(ctx context.Context, plan *Plan, root string) error {
	if err := snapshotContext(ctx); err != nil {
		return err
	}
	if plan == nil || plan.RuntimeInputs == nil || root == "" {
		return e.New(e.CodeUsage, "frozen input check requires Plan, snapshot and checkout")
	}
	if err := plan.validateFrozenSources(); err != nil {
		return err
	}
	state := newInputState()
	state.source = catalog.Dir(root).(catalog.ContextDataProvider)
	for _, file := range plan.RuntimeInputs.Files {
		raw, err := state.readFile(ctx, file.Path)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, file.Data) {
			return e.New(e.CodePipelineDrift, "frozen authored file %q differs from workflow checkout", file.Path)
		}
	}
	for _, directory := range plan.RuntimeInputs.Directories {
		entries, err := state.readDirectory(ctx, directory.Path)
		if err != nil {
			return err
		}
		if !slices.Equal(entries, directory.Entries) {
			return e.New(e.CodePipelineDrift, "frozen input directory %q differs from workflow checkout", directory.Path)
		}
	}
	return nil
}

// FreezeRuntimeSources admits the entire baseline before observing any main
// ref. The returned Plan binds all authored bytes and exact source observations.
// Only the trusted coordinator bootstrap calls this; workers restore its same-Run Plan.
func FreezeRuntimeSources(ctx context.Context, root, definitionPath, environmentPath, version string) (*Plan, error) {
	baseline, inputs, err := CaptureCompileFiles(ctx, root, definitionPath, environmentPath, version)
	if err != nil {
		return nil, err
	}
	if len(baseline.Environment.GitHubRuntimeSources) == 0 {
		return nil, e.New(e.CodeEngineCapability, "source freeze requires an explicit repository allowlist")
	}
	if err := PrepareGitHubRuntimeBundle(ctx, baseline, root, true); err != nil {
		return nil, err
	}
	savedPath, err := generatedDestination(root, githubLayoutFor(baseline).planPath)
	if err != nil {
		return nil, err
	}
	saved, err := LoadPlanContext(ctx, savedPath)
	if err != nil {
		return nil, err
	}
	if saved.RuntimeInputs != nil {
		if err := CheckFrozenAuthoredInputs(ctx, saved, root); err != nil {
			return nil, err
		}
		return CompileSnapshot(ctx, *saved.RuntimeInputs, version)
	}
	selections := make([]catalog.SourceSelection, 0, len(baseline.Environment.GitHubRuntimeSources))
	for _, source := range baseline.Environment.GitHubRuntimeSources {
		selections = append(selections, catalog.SourceSelection{Repository: source.Repository, Ref: source.Ref})
	}
	if err := validateSourcePolicy(baseline, selections); err != nil {
		return nil, err
	}
	for _, source := range selections {
		frozen, err := catalog.FreezeSourceHead(ctx, source.Repository, source.Ref)
		if err != nil {
			return nil, err
		}
		inputs.Sources = append(inputs.Sources, frozen)
	}
	return CompileSnapshot(ctx, inputs, version)
}

func validateSourcePolicy(plan *Plan, selections []catalog.SourceSelection) error {
	policy := plan.Environment.GitHubRuntimeSources
	if len(policy) == 0 || len(policy) != len(selections) || plan.RuntimeSourceCommit != "" {
		return e.New(e.CodeUnpinnedSource, "frozen sources differ from the complete runtime source policy")
	}
	for i, selection := range selections {
		if selection.Repository != policy[i].Repository || selection.Ref != policy[i].Ref {
			return e.New(e.CodeUnpinnedSource, "frozen sources differ from the ordered repository/ref allowlist")
		}
		matched := false
		for _, build := range plan.Builds {
			for _, component := range build.ResolvedComponents {
				if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == selection.Repository {
					matched = true
				}
			}
		}
		if !matched {
			return e.New(e.CodeUnpinnedSource, "runtime source %q matches no selected from-source producer", selection.Repository)
		}
	}
	return nil
}

func (plan Plan) validateFrozenSources() error {
	if plan.RuntimeInputs == nil {
		return nil
	} // Committed generation baseline.
	if err := validateInputSnapshot(*plan.RuntimeInputs); err != nil {
		return err
	}
	if plan.RuntimeInputs.DefinitionPath != plan.DefinitionPath || plan.RuntimeInputs.EnvironmentPath != plan.EnvironmentPath {
		return e.New(e.CodeUnverifiedRef, "frozen inputs change the selected authored paths")
	}
	if err := validateSourcePolicy(&plan, plan.RuntimeInputs.Sources); err != nil {
		return err
	}
	for _, source := range plan.RuntimeInputs.Sources {
		if !runtimeSourceCommitValid(source.Commit) || !runtimeSourceCommitValid(source.TreeHash) || source.SourceDateEpoch <= 0 {
			return e.New(e.CodeUnpinnedSource, "frozen source requires exact commit, tree and timestamp")
		}
		for _, build := range plan.Builds {
			for _, component := range build.ResolvedComponents {
				if !component.FromSource || strings.TrimSuffix(component.Repo, ".git") != source.Repository {
					continue
				}
				if component.Commit != source.Commit || component.Tag != source.Commit || component.TreeHash != source.TreeHash || component.SourceDate != source.SourceDateEpoch {
					return e.New(e.CodeUnpinnedSource, "output %q component %q differs from its frozen source", build.Name, component.Name)
				}
			}
		}
	}
	return nil
}

func admitRuntimeSourcesExecution(plan *Plan) error {
	if len(plan.Environment.GitHubRuntimeSources) != 0 && plan.RuntimeInputs == nil {
		return e.New(e.CodeUnpinnedSource, "runtime source baseline cannot execute before source freeze")
	}
	return plan.validateFrozenSources()
}
