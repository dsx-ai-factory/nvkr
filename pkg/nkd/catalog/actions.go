// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"regexp"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// SourceGitHubAction marks a workflow dependency resolved by catalog pin/bump.
const SourceGitHubAction = "github-action"

// ActionPin is the resolved upstream release and immutable workflow reference.
type ActionPin struct {
	Repo    string `json:"repo"`
	Version string `json:"version"`
	SHA     string `json:"sha"`
}

var actionRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var actionVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Uses returns the exact reference; callers validate before rendering.
func (p ActionPin) Uses() string { return p.Repo + "@" + p.SHA }

func (p ActionPin) Validate() error {
	if !fullSHA.MatchString(p.SHA) {
		return e.New(e.CodeUnpinnedSource, "GitHub action %q requires a full commit SHA", p.Repo)
	}
	if !actionRepo.MatchString(p.Repo) || strings.HasSuffix(p.Repo, ".git") || !actionVersion.MatchString(p.Version) {
		return e.New(e.CodeInvalidCatalog, "GitHub action requires owner/repository and an exact stable release tag: %q %q", p.Repo, p.Version)
	}
	return nil
}

func validateSourceKind(source Source) error {
	switch source.Kind {
	case "":
		return nil
	case SourceGitHubAction:
		repo, ok := strings.CutPrefix(source.Repo, "https://github.com/")
		if !ok || !actionRepo.MatchString(repo) || strings.HasSuffix(repo, ".git") || source.Tool != nil || source.TagPrefix != "" || source.ToolchainImage != "" || source.ToolchainBaseImage != "" || len(source.ToolchainImagesByArch) > 0 || len(source.VendorCommand) > 0 {
			return e.New(e.CodeInvalidCatalog, "GitHub action source requires a canonical GitHub repository and no build/tool settings")
		}
		return nil
	default:
		return e.New(e.CodeInvalidCatalog, "unknown source kind %q", source.Kind)
	}
}
