// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"net/url"
	"path"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// GitHubRuntimeSource authorizes exact-commit selection for one repository.
// Ref records the coordinator's selection policy, never a mutable build input.
type GitHubRuntimeSource struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
}

func (environment Environment) validateGitHubRuntimeSource() error {
	selection := environment.GitHubRuntimeSource
	if selection == nil {
		return nil
	}
	if !environment.GitHubRuntimePlan || environment.GitHubBundle == "" || environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "githubRuntimeSource requires a named production runtime Plan")
	}
	u, err := url.Parse(selection.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" ||
		u.Path != path.Clean(u.Path) || strings.HasSuffix(u.Path, ".git") || u.String() != selection.Repository {
		return e.New(e.CodeInvalidCatalog, "githubRuntimeSource requires a canonical HTTPS GitHub repository without credentials or a .git suffix")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(u.Path, " \t\r\n\\") || selection.Ref != "refs/heads/main" {
		return e.New(e.CodeInvalidCatalog, "githubRuntimeSource requires one repository and exact refs/heads/main selection")
	}
	return nil
}
