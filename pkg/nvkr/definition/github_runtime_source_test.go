// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubRuntimeSourcePolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{"exact main source", func(*Environment) {}, ""},
		{"missing runtime owner", func(v *Environment) { v.GitHubRuntimePlan = false }, e.CodeEngineCapability},
		{"unnamed bundle", func(v *Environment) { v.GitHubBundle = "" }, e.CodeEngineCapability},
		{"development", func(v *Environment) { v.GitHubDevelopment = true }, e.CodeEngineCapability},
		{"mutable alternative ref", func(v *Environment) { v.GitHubRuntimeSource.Ref = "main" }, e.CodeInvalidCatalog},
		{"foreign host", func(v *Environment) { v.GitHubRuntimeSource.Repository = "https://example.invalid/org/repo" }, e.CodeInvalidCatalog},
		{"credential URL", func(v *Environment) { v.GitHubRuntimeSource.Repository = "https://user@github.com/org/repo" }, e.CodeInvalidCatalog},
		{"git suffix alias", func(v *Environment) { v.GitHubRuntimeSource.Repository += ".git" }, e.CodeInvalidCatalog},
		{"query", func(v *Environment) { v.GitHubRuntimeSource.Repository += "?source=other" }, e.CodeInvalidCatalog},
		{"path traversal", func(v *Environment) { v.GitHubRuntimeSource.Repository = "https://github.com/org/../repo" }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := Environment{GitHubRuntimePlan: true, GitHubBundle: "nightly", GitHubRuntimeSource: &GitHubRuntimeSource{Repository: "https://github.com/org/repo", Ref: "refs/heads/main"}}
			test.edit(&value)
			err := value.validateGitHubRuntimeSource()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				clone := value.Clone()
				clone.GitHubRuntimeSource.Repository = "https://github.com/other/repo"
				if value.GitHubRuntimeSource.Repository != "https://github.com/org/repo" {
					t.Fatal("clone shares source policy")
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("policy returned %v, want %s", err, test.code)
			}
		})
	}
}
