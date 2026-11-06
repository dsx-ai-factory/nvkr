// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// qualificationNamespace separates each producer's ownership, lock and stages.
func qualificationNamespace(producer string) (directory, qualifier, smoke string, err error) {
	if producer == factory.GitHubWorkflowPath {
		return qualificationOwnerDirectory, "", "", nil
	}
	if strings.HasPrefix(producer, ".github/workflows/nvkr-dev") {
		bundle, parseErr := development.BundleForProducer(producer)
		if parseErr != nil {
			return "", "", "", parseErr
		}
		qualifier, smoke, err = development.QualificationWorkflows(producer)
		directory = ".github/nvkr-dev-qualification"
		if bundle != "" {
			directory += "-" + bundle
		}
		return directory, qualifier, smoke, err
	}
	name, ok := strings.CutPrefix(producer, ".github/workflows/nvkr-run-")
	if !ok || !strings.HasSuffix(name, ".yml") {
		return "", "", "", e.New(e.CodeEngineCapability, "qualification requires a generated producer workflow")
	}
	name = strings.TrimSuffix(name, ".yml")
	if name == "" || definition.ValidateGitHubBundleName(name) != nil {
		return "", "", "", e.New(e.CodeEngineCapability, "invalid named qualification producer")
	}
	return ".github/nvkr-qualification-" + name, ".github/workflows/nvkr-qualify-" + name + ".yml", ".github/workflows/nvkr-smoke-" + name + ".yml", nil
}

// ManifestPath identifies this generated pair's finite ownership directory.
func (bundle *GeneratedWorkflows) ManifestPath() string { return bundle.manifest.manifestPath() }

// The default owner retains authored workflow names outside every other generated namespace.
func defaultQualificationPath(path string) bool {
	for _, prefix := range []string{".github/workflows/nvkr-run", ".github/workflows/nvkr-dev", ".github/workflows/nvkr-qualify-", ".github/workflows/nvkr-smoke-"} {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}
