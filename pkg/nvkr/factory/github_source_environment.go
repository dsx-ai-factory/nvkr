// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// GitHubCatalogSourceEnvironment renders catalog revalidation credentials from a
// trusted generation Plan. It is pure rendering data, never candidate authority;
// callers must scope the returned environment to their catalog-reading step.
func GitHubCatalogSourceEnvironment(plan *Plan) (map[string]string, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "source environment requires a trusted generation Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return githubCatalogSourceEnvironment(plan), nil
}
