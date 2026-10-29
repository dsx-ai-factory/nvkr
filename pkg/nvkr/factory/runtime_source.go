// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CompileRuntimeSourceDocuments authenticates one selected source tree, then
// recompiles the unchanged authored selection through catalog-owned overlays.
// No lock, registry, authored overlay or producer command is rewritten.
func CompileRuntimeSourceDocuments(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, environment authored.Environment, generatorVersion, commit string,
) (*Plan, error) {
	if err := admitInputs(definition, environment); err != nil {
		return nil, err
	}
	if environment.GitHubRuntimeSource == nil || !runtimeSourceCommitValid(commit) {
		return nil, e.New(e.CodeEngineCapability, "exact runtime source compilation requires its authored production policy and a full commit")
	}
	// Reject malformed original producer graphs before source acquisition.
	if _, err := CompileDocuments(ctx, definitionPath, definition, environmentPath, environment, generatorVersion); err != nil {
		return nil, err
	}
	revision, err := catalog.ResolveSourceRevisionAtRef(ctx, environment.GitHubRuntimeSource.Repository, environment.GitHubRuntimeSource.Ref, commit)
	if err != nil {
		return nil, err
	}
	return compileDocumentsWithLoader(ctx, definitionPath, definition, environmentPath, environment, generatorVersion,
		func(ctx context.Context, selection authored.Catalog) (*catalog.Stack, error) {
			stack, err := loadFactoryCatalogStack(ctx, definitionPath, selection)
			if err != nil {
				return nil, err
			}
			return catalog.ApplySourceRevision(stack, revision)
		}, commit)
}

func runtimeSourceCommitValid(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil && strings.ToLower(commit) == commit
}

func (plan Plan) validateRuntimeSourceSelection() error {
	if plan.RuntimeSourceCommit == "" {
		return nil // A committed generation baseline is not a frozen attempt.
	}
	policy := plan.Environment.GitHubRuntimeSource
	if policy == nil || !runtimeSourceCommitValid(plan.RuntimeSourceCommit) || !plan.Environment.GitHubRuntimePlan || plan.Environment.GitHubDevelopment || plan.Environment.GitHubBundle == "" {
		return e.New(e.CodeEngineCapability, "frozen runtime source lacks its exact production policy or full commit")
	}
	matched := 0
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if !component.FromSource || strings.TrimSuffix(component.Repo, ".git") != policy.Repository {
				continue
			}
			matched++
			if component.Commit != plan.RuntimeSourceCommit || component.Tag != plan.RuntimeSourceCommit {
				return e.New(e.CodeUnpinnedSource, "output %q component %q differs from the frozen runtime source commit", build.Name, component.Name)
			}
		}
	}
	if matched == 0 {
		return e.New(e.CodeUnpinnedSource, "frozen runtime source policy matches no selected from-source producer")
	}
	return nil
}
