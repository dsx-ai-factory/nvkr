// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"maps"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// GitLab configures explicit native runner selection. No private organization
// labels, credentials or default architecture routes are inferred.
type GitLab struct {
	APIURL                string                  `json:"apiURL"`
	ProjectID             uint64                  `json:"projectID"`
	APIReadCredentialRef  string                  `json:"apiReadCredentialRef"`
	APIReadCredentialKind string                  `json:"apiReadCredentialKind"`
	Runners               map[string]GitLabRunner `json:"runners"`
}

// GitLabRunner declares scheduling selectors and an immutable bootstrap image.
// Its actual platform, Go installation and runtime remain execution preflights.
type GitLabRunner struct {
	Tags           []string `json:"tags"`
	BootstrapImage string   `json:"bootstrapImage"`
}

func (environment Environment) validateGitLab() error {
	if environment.GitLab == nil {
		return nil
	}
	policy := environment.GitLab
	api, err := url.Parse(policy.APIURL)
	if err != nil || api.Scheme != "https" || api.Host == "" || api.User != nil ||
		api.RawQuery != "" || api.Fragment != "" || api.Opaque != "" ||
		api.RawPath != "" || api.Path != "/api/v4" || strings.ContainsAny(policy.APIURL, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "gitlab.apiURL requires an HTTPS API base ending in /api/v4 without credentials, query or fragment")
	}
	if policy.ProjectID == 0 {
		return e.New(e.CodeInvalidCatalog, "gitlab.projectID requires the exact positive native project ID")
	}
	// Reuse the established credential-variable grammar; only the provider
	// namespace changes. This validates a reference, never a credential value.
	variable, ok := strings.CutPrefix(policy.APIReadCredentialRef, "gitlab:")
	if !ok || !githubCredentialRefPattern.MatchString("github:"+variable) {
		return e.New(e.CodeInvalidCatalog, "gitlab.apiReadCredentialRef must be gitlab:VARIABLE_NAME")
	}
	if policy.APIReadCredentialKind != "job-token" && policy.APIReadCredentialKind != "private-token" {
		return e.New(e.CodeEngineCapability, "GitLab API credential kind must explicitly be job-token or private-token")
	}
	if environment.RunsOn != "" || environment.GitHubBundle != "" || len(environment.GitHubRunners) != 0 ||
		environment.GitHubDevelopment || environment.GitHubRuntimePlan || environment.GitHubExecutionPartitions ||
		environment.GitHubRuntimeSource != nil || len(environment.GitHubRuntimeSources) != 0 ||
		environment.GitHubARC != "" || len(environment.GitHubDispatchBranches) != 0 {
		return e.New(e.CodeEngineCapability, "GitLab runner policy cannot be combined with GitHub runner or workflow policy")
	}
	runners := environment.GitLab.Runners
	if len(runners) == 0 || len(runners) > 2 {
		return e.New(e.CodeInvalidCatalog, "gitlab.runners requires one or two explicit native platform routes")
	}
	for _, platform := range slices.Sorted(maps.Keys(runners)) {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return e.New(e.CodeEngineCapability, "GitLab execution platform %q is unsupported", platform)
		}
		runner := runners[platform]
		if len(runner.Tags) == 0 || len(runner.Tags) > 16 {
			return e.New(e.CodeInvalidCatalog, "GitLab runner %s requires one to sixteen explicit tags", platform)
		}
		previous := ""
		for _, tag := range runner.Tags {
			if tag == "" || len(tag) > 128 || tag <= previous || strings.TrimSpace(tag) != tag ||
				strings.ContainsAny(tag, "$`\\") || strings.ContainsFunc(tag, unicode.IsControl) {
				return e.New(e.CodeInvalidCatalog, "GitLab runner tags must be bounded, sorted, unique literal selectors")
			}
			previous = tag
		}
		image, err := name.NewDigest(runner.BootstrapImage, name.StrictValidation)
		if err != nil || image.Name() != runner.BootstrapImage || !canonical.IsDigest(image.DigestStr()) ||
			strings.ContainsAny(runner.BootstrapImage, "\r\n\x00") {
			return e.New(e.CodeUnpinnedSource, "GitLab runner %s bootstrapImage must be a digest-pinned OCI image", platform)
		}
	}
	return nil
}
