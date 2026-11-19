// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"strings"

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
		if err := admitRuntimeGeneration(ctx, environmentPath, check, sourceCommit); err != nil {
			return "", err
		}
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(engine), factory.EngineTekton) {
		if err := ValidateTektonPlan(plan); err != nil {
			return "", err
		}
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
		plan, err = freezeRuntimeGeneration(ctx, plan, root, definitionPath, environmentPath, generatorVersion, check)
		if err != nil {
			return "", err
		}
		if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, check); err != nil {
			return "", err
		}
		// A secondary bootstrap runs this after installing the restored runtime
		// inputs, so the committed bundle already holds the frozen attempt. The
		// exact check verifies that attempt and rejects a different dispatch source.
		if check && len(sourceCommit) != 0 {
			if err := factory.CheckGenerated(ctx, plan, engine, root); err != nil {
				return "", err
			}
		}
		return plan.Digest()
	}
	return factory.Generate(ctx, plan, engine, root, check)
}

// CompileRuntimeGeneration preserves the runtime owner's input admission and
// source freeze for coordinated generation. It writes no generated bundle;
// the caller must admit the compiled Plan against its committed provider bundle.
func CompileRuntimeGeneration(ctx context.Context, definitionPath, environmentPath, generatorVersion, root string, check bool) (*factory.Plan, error) {
	if err := admitRuntimeGeneration(ctx, environmentPath, check, nil); err != nil {
		return nil, err
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return nil, err
	}
	return freezeRuntimeGeneration(ctx, plan, root, definitionPath, environmentPath, generatorVersion, check)
}

func admitRuntimeGeneration(ctx context.Context, environmentPath string, check bool, sourceCommit []string) error {
	if factory.ProjectionAdapterSelected(ctx) {
		return e.New(e.CodeEngineCapability, "runtime Plan refresh is unavailable for a selected projection adapter")
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return err
	}
	if !environment.GitHubRuntimePlan {
		return e.New(e.CodeEngineCapability, "runtime Plan preparation requires githubRuntimePlan")
	}
	if environment.GitHubRuntimeSource != nil && len(sourceCommit) == 0 && !check {
		return e.New(e.CodeUnpinnedSource, "runtime source preparation requires an exact source commit")
	}
	return nil
}

func freezeRuntimeGeneration(ctx context.Context, plan *factory.Plan, root, definitionPath, environmentPath, generatorVersion string, check bool) (*factory.Plan, error) {
	if len(plan.Environment.GitHubRuntimeSources) != 0 && !check {
		return factory.FreezeRuntimeSources(ctx, root, definitionPath, environmentPath, generatorVersion)
	}
	return plan, nil
}
