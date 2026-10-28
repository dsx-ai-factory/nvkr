// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// Count the expanded provider operations, not graph artifacts or distinct names.
// Bootstrap, conditional diagnostics and finalization can also create locators;
// every possible upload must fit the pinned downloader's admitted listing bound.
func validateGitHubLocatorBudget(document githubWorkflowDocument, actions githubActionRefs) error {
	count := 0
	for _, job := range document.Jobs {
		for _, step := range job.Steps {
			if step.Uses == actions.Upload {
				count++
				if count > maxGitHubLocatorArtifacts {
					return e.New(e.CodeEngineCapability, "generated workflow exceeds the private locator discovery limit of %d possible artifact uploads", maxGitHubLocatorArtifacts)
				}
			}
		}
	}
	return nil
}
