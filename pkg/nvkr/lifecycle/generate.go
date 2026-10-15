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

func generateFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check, runtimePlan bool) (string, error) {
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
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return "", err
	}
	if runtimePlan {
		if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, check); err != nil {
			return "", err
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
