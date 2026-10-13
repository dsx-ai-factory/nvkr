// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"regexp"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var githubBundleNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,46}[a-z0-9])?$`)

// ValidateGitHubBundleName admits only an optional bounded filename token.
// This is namespace selection, not an authored path or workflow authority.
func ValidateGitHubBundleName(value string) error {
	if value != "" && !githubBundleNamePattern.MatchString(value) {
		return e.New(e.CodeInvalidCatalog, "githubBundle must be at most 48 lowercase letters, digits or hyphens with alphanumeric ends")
	}
	return nil
}
