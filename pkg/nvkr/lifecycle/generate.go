// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// GenerateFiles renders or checks one hosted Run from authored lifecycle
// inputs. The generated Plan remains an inspectable internal record.
func GenerateFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check bool) (string, error) {
	return generateFiles(ctx, definitionPath, environmentPath, generatorVersion, engine, root, check, false)
}

// GenerateRuntimeFiles admits the reviewed provider contract before refreshing
// the dispatch's pinned Plan data. Check mode admits that contract without writes.
func GenerateRuntimeFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check bool) (string, error) {
	return generateFiles(ctx, definitionPath, environmentPath, generatorVersion, engine, root, check, true)
}

// GenerateRuntimeSourceFiles freezes an explicitly selected exact source commit
// within the reviewed runtime workflow. Branch resolution belongs to its caller.
func GenerateRuntimeSourceFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root, commit string, check bool) (string, error) {
	if commit == "" {
		return "", e.New(e.CodeUsage, "runtime source generation requires an exact source commit")
	}
	return generateFiles(ctx, definitionPath, environmentPath, generatorVersion, engine, root, check, true, commit)
}

// GenerateSourceOverrideFiles freezes the dispatch's source overrides within
// the reviewed development runtime workflow. raw is the dispatch input object;
// empty values keep the reviewed pins. Check mode replays the installed Plan's
// frozen selection instead of resolving constraints again.
func GenerateSourceOverrideFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root, raw string, check bool) (string, error) {
	if err := factory.ValidateGenerationEngine(engine); err != nil {
		return "", err
	}
	if engine != factory.EngineGitHub {
		return "", e.New(e.CodeUsage, "source overrides require the GitHub engine")
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return "", err
	}
	if !environment.GitHubDevelopment || environment.Overridable == nil {
		return "", e.New(e.CodeEngineCapability, "dispatch source overrides require a development Environment with overridable sources")
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return "", err
	}
	requests, err := factory.GitHubSourceOverrideRequests(plan, raw)
	if err != nil {
		return "", err
	}
	// Admit the entire committed or installed bundle before acquiring sources.
	if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, true); err != nil {
		return "", err
	}
	var installed *factory.Plan
	if check {
		if installed, err = factory.LoadGeneratedGitHubPlan(ctx, plan, root); err != nil {
			return "", err
		}
		if len(installed.SourceOverrides) != len(requests) {
			return "", e.New(e.CodePipelineDrift, "installed Plan does not freeze this dispatch's source overrides")
		}
		for repository, request := range requests {
			if installed.SourceOverrides[repository].Request != request {
				return "", e.New(e.CodePipelineDrift, "installed Plan does not freeze this dispatch's override of %s", repository)
			}
		}
	}
	if len(requests) != 0 {
		if check {
			plan, err = ReplayDevelopmentSourceOverrideFiles(ctx, definitionPath, environmentPath, generatorVersion, installed.SourceOverrides)
		} else {
			plan, err = CompileDevelopmentSourceOverrideFiles(ctx, definitionPath, environmentPath, generatorVersion, requests)
		}
		if err != nil {
			return "", err
		}
	}
	if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, check); err != nil {
		return "", err
	}
	if check {
		bundle, err := factory.Render(ctx, plan, engine)
		if err != nil {
			return "", err
		}
		if err := factory.CheckGeneratedBundle(ctx, root, bundle); err != nil {
			return "", err
		}
	}
	return plan.Digest()
}

func generateFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check, runtimePlan bool, sourceCommit ...string) (string, error) {
	if err := factory.ValidateGenerationEngine(engine); err != nil {
		return "", err
	}
	if runtimePlan {
		environment, err := LoadEnvironment(ctx, environmentPath)
		if err != nil {
			return "", err
		}
		if !environment.UsesGitHubRuntimePlan() {
			return "", e.New(e.CodeEngineCapability, "runtime Plan preparation requires githubRuntimePlan or development overridable sources")
		}
		if environment.GitHubRuntimeSource != nil && len(sourceCommit) == 0 && !check {
			return "", e.New(e.CodeUnpinnedSource, "runtime source preparation requires an exact source commit")
		}
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return "", err
	}
	if len(sourceCommit) != 0 {
		if len(sourceCommit) != 1 || !runtimePlan || engine != factory.EngineGitHub {
			return "", e.New(e.CodeUsage, "exact source selection requires one GitHub runtime source commit")
		}
		// Admit the entire committed executable bundle before acquiring another
		// source tree. The subsequent owner repeats this before any writes.
		if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, true); err != nil {
			return "", err
		}
		plan, err = CompileRuntimeSourceFiles(ctx, definitionPath, environmentPath, generatorVersion, sourceCommit[0])
		if err != nil {
			return "", err
		}
	}
	if runtimePlan {
		if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, check); err != nil {
			return "", err
		}
		if check && len(sourceCommit) != 0 {
			bundle, err := factory.Render(ctx, plan, engine)
			if err != nil {
				return "", err
			}
			if err := factory.CheckGeneratedBundle(ctx, root, bundle); err != nil {
				return "", err
			}
		}
		return plan.Digest()
	}
	bundle, err := factory.Render(ctx, plan, engine)
	if err != nil {
		return "", err
	}
	if check {
		if err := factory.CheckGeneratedBundle(ctx, root, bundle); err != nil {
			return "", err
		}
	} else if err := factory.WriteGeneratedBundle(ctx, root, bundle); err != nil {
		return "", err
	}
	return bundle.PlanDigest, nil
}
