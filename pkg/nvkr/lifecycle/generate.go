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

func generateFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check, runtimePlan bool, sourceCommit ...string) (string, error) {
	if err := factory.ValidateGenerationEngine(engine); err != nil {
		return "", err
	}
	if runtimePlan {
		environment, err := LoadEnvironment(ctx, environmentPath)
		if err != nil {
			return "", err
		}
		if !environment.GitHubRuntimePlan {
			return "", e.New(e.CodeEngineCapability, "runtime Plan preparation requires githubRuntimePlan")
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
