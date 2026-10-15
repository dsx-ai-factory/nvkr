// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path"
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

var githubWorkflowTemplateName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// IsGeneratedGitHubChildWorkflowPath identifies the reserved producer-owned
// workflow namespace. It grants no execution authority or ownership of bytes.
func IsGeneratedGitHubChildWorkflowPath(relative string) bool {
	_, ok := githubChildWorkflowOwner(relative)
	return ok
}

func githubChildWorkflowOwner(relative string) (string, bool) {
	if !safeGeneratedPath(relative) || path.Dir(relative) != ".github/workflows" || !strings.HasSuffix(relative, ".yml") {
		return "", false
	}
	stem, template, ok := strings.Cut(strings.TrimSuffix(relative, ".yml"), "__")
	if !ok || !githubWorkflowTemplateName.MatchString(template) {
		return "", false
	}
	entry := stem + ".yml"
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

func githubWorkflowPathInNamespace(entry, member string) bool {
	if _, ok := githubWorkflowNamespace(entry); !ok {
		return false
	}
	if member == entry {
		return true
	}
	owner, ok := githubChildWorkflowOwner(member)
	return ok && owner == entry
}

func generatedMemberInNamespace(bundle *GeneratedBundle, member string) bool {
	if !safeGeneratedPath(member) || member == bundle.ManifestPath || member == bundle.ManifestPath+".lease" {
		return false
	}
	return githubWorkflowPathInNamespace(bundle.WorkflowPath, member) || strings.HasPrefix(member, path.Dir(bundle.ManifestPath)+"/")
}
