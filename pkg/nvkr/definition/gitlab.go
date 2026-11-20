// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"crypto/x509"
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
	CertificateAuthority  string                  `json:"certificateAuthority,omitempty"`
	AdditionalRefs        []string                `json:"additionalRefs,omitempty"`
	PipelineSources       []string                `json:"pipelineSources,omitempty"`
	Runners               map[string]GitLabRunner `json:"runners"`
}

// AdmittedPipelineSources returns the exact authored sources, or the bounded
// push/manual defaults. Callers receive their own copy, never mutable policy.
func (policy GitLab) AdmittedPipelineSources() []string {
	if len(policy.PipelineSources) == 0 {
		return []string{"push", "web"}
	}
	return slices.Clone(policy.PipelineSources)
}

// GitLabBranchNameAdmitted selects the same bounded literal branch-name subset
// as authored GitHub dispatch policy. Git itself remains the ref-syntax owner.
func GitLabBranchNameAdmitted(ref string) bool {
	if !dispatchBranchPattern.MatchString(ref) || len(ref) > 255 || strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
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
	if err := validateHTTPSURL("gitlab.apiURL", policy.APIURL); err != nil {
		return e.New(e.CodeInvalidCatalog, "gitlab.apiURL requires an HTTPS API base ending in /api/v4 without credentials, query or fragment")
	}
	api, _ := url.Parse(policy.APIURL) // The shared URL owner admitted canonical HTTPS above.
	if api.Hostname() == "" || api.RawPath != "" || api.Path != "/api/v4" || strings.ContainsAny(policy.APIURL, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "gitlab.apiURL requires an HTTPS API base ending in /api/v4 without credentials, query or fragment")
	}
	if policy.ProjectID == 0 {
		return e.New(e.CodeInvalidCatalog, "gitlab.projectID requires the exact positive native project ID")
	}
	if policy.CertificateAuthority != "" && (len(policy.CertificateAuthority) > 64<<10 || !x509.NewCertPool().AppendCertsFromPEM([]byte(policy.CertificateAuthority))) {
		return e.New(e.CodeTrustPolicyMissing, "gitlab.certificateAuthority must contain a bounded explicit PEM certificate authority")
	}
	if len(policy.AdditionalRefs) > 32 || len(policy.PipelineSources) > 16 {
		return e.New(e.CodeInvalidCatalog, "GitLab invocation policy exceeds its exact ref/source bounds")
	}
	previousRef := ""
	for _, ref := range policy.AdditionalRefs {
		// Reuse the existing authored dispatch-branch subset. Git remains the
		// owner of Git ref syntax; these are literal policy selectors, not globs.
		if !GitLabBranchNameAdmitted(ref) || ref <= previousRef {
			return e.New(e.CodeInvalidCatalog, "GitLab additionalRefs must be sorted, unique exact branch names")
		}
		previousRef = ref
	}
	previousSource := ""
	for _, source := range policy.PipelineSources {
		if source <= previousSource || !slices.Contains([]string{"api", "merge_request_event", "parent_pipeline", "pipeline", "push", "schedule", "trigger", "web", "webide"}, source) {
			return e.New(e.CodeEngineCapability, "GitLab pipelineSources must be sorted, unique supported native sources")
		}
		previousSource = source
	}
	// Reuse the established credential-variable grammar; only the provider
	// namespace changes. This validates a reference, never a credential value.
	variable, ok := strings.CutPrefix(policy.APIReadCredentialRef, "gitlab:")
	if !ok || !githubCredentialRefPattern.MatchString("github:"+variable) || variable == "CI_JOB_TOKEN" || variable == "CI_REGISTRY_PASSWORD" {
		return e.New(e.CodeInvalidCatalog, "gitlab.apiReadCredentialRef must name a separately provisioned gitlab:VARIABLE_NAME reader")
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
			strings.ContainsAny(runner.BootstrapImage, "$\r\n\x00") {
			return e.New(e.CodeUnpinnedSource, "GitLab runner %s bootstrapImage must be a digest-pinned OCI image", platform)
		}
	}
	return nil
}
