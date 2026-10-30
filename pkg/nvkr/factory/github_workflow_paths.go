// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path"
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
)

var legacyGitHubWorkflowTemplateName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var githubPartitionWorkflowSuffix = regexp.MustCompile(`__partition-[0-4][0-9]\.yml$`)

// IsLegacyGeneratedGitHubChildWorkflowPath identifies names reserved for retiring
// prior producer-owned workflows, including the admitted partition subset.
func IsLegacyGeneratedGitHubChildWorkflowPath(relative string) bool {
	_, ok := legacyGitHubChildWorkflowOwner(relative)
	return ok
}

func legacyGitHubChildWorkflowOwner(relative string) (string, bool) {
	if !safeGeneratedPath(relative) || path.Dir(relative) != ".github/workflows" || !strings.HasSuffix(relative, ".yml") {
		return "", false
	}
	stem, template, ok := strings.Cut(strings.TrimSuffix(relative, ".yml"), "__")
	if !ok || !legacyGitHubWorkflowTemplateName.MatchString(template) {
		return "", false
	}
	entry := stem + ".yml"
	// Named development bundles postdate legacy producer children; they own
	// only their execution partition workers.
	if strings.HasPrefix(entry, ".github/workflows/nvkr-dev-") && !githubPartitionWorkflowSuffix.MatchString(relative) {
		return "", false
	}
	_, ok = githubWorkflowNamespace(entry)
	return entry, ok
}

func githubWorkflowNamespace(entry string) (generationLayout, bool) {
	if entry == GitHubWorkflowPath {
		return githubBundleLayout(""), true
	}
	if entry == GitHubDevelopmentWorkflowPath {
		return githubDevelopmentLayout, true
	}
	if name, ok := strings.CutPrefix(entry, ".github/workflows/nvkr-dev-"); ok && strings.HasSuffix(name, ".yml") {
		name = strings.TrimSuffix(name, ".yml")
		if definition.ValidateGitHubBundleName(name) != nil || development.ValidateBundle(name) != nil {
			return generationLayout{}, false
		}
		return githubDevelopmentBundleLayout(name), true
	}
	const prefix = ".github/workflows/nvkr-run-"
	if !strings.HasPrefix(entry, prefix) || !strings.HasSuffix(entry, ".yml") {
		return generationLayout{}, false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(entry, prefix), ".yml")
	if name == "" || definition.ValidateGitHubBundleName(name) != nil {
		return generationLayout{}, false
	}
	return githubBundleLayout(name), true
}

func generatedMemberInNamespace(bundle *GeneratedBundle, member string) bool {
	if !safeGeneratedPath(member) || member == bundle.ManifestPath || member == bundle.ManifestPath+".lease" {
		return false
	}
	owner, sibling := legacyGitHubChildWorkflowOwner(member)
	return member == bundle.WorkflowPath || strings.HasPrefix(member, path.Dir(bundle.ManifestPath)+"/") ||
		(sibling && owner == bundle.WorkflowPath && githubPartitionWorkflowSuffix.MatchString(member))
}

// Legacy child paths are admitted only from a prior manifest for retirement.
func priorGeneratedMemberInNamespace(bundle *GeneratedBundle, member string) bool {
	if generatedMemberInNamespace(bundle, member) {
		return true
	}
	owner, ok := legacyGitHubChildWorkflowOwner(member)
	return ok && owner == bundle.WorkflowPath
}
