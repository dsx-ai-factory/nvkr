// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// QualificationResultsModeTestGridGCS publishes qualification outcomes to an
// object store that a TestGrid dashboard reads (ADR-31).
const QualificationResultsModeTestGridGCS = "testgrid-gcs"

// The repository's other protected GitHub Environments: the factory's release, ARC
// and development Environments, and the development cleanup workflow's. They carry
// other jobs' credentials and deployment policies, so the publish job never shares
// one. The factory imports this package, so its names are restated here.
var reservedGitHubEnvironments = []string{"nvkr-arc", "nvkr-dev-arc", "nvkr-dev-cleanup", "nvkr-dev-release", "nvkr-release"}

var (
	githubEnvironmentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)
	githubSecretRefPattern       = regexp.MustCompile(`^github:[A-Z][A-Z0-9_]{0,99}$`)
)

// QualificationResults declares where the generated release qualifier publishes
// its outcomes (ADR-31 §7). Every reference names a secret of GitHubEnvironment,
// so neither the store nor the identity appears in the generated workflow.
type QualificationResults struct {
	Mode                        string `json:"mode"`
	GitHubEnvironment           string `json:"githubEnvironment"`
	BucketRef                   string `json:"bucketRef"`
	WorkloadIdentityProviderRef string `json:"workloadIdentityProviderRef"`
	ServiceAccountRef           string `json:"serviceAccountRef"`
}

// SecretRefs returns the bucket, identity provider and service account secret names.
func (results QualificationResults) SecretRefs() (bucket, provider, account string) {
	return strings.TrimPrefix(results.BucketRef, "github:"), strings.TrimPrefix(results.WorkloadIdentityProviderRef, "github:"),
		strings.TrimPrefix(results.ServiceAccountRef, "github:")
}

func (environment Environment) validateQualificationResults() error {
	results := environment.QualificationResults
	if results == nil {
		return nil
	}
	if environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "qualificationResults publishes production qualification only")
	}
	if results.Mode != QualificationResultsModeTestGridGCS {
		return e.New(e.CodeInvalidCatalog, "qualificationResults mode must be %s", QualificationResultsModeTestGridGCS)
	}
	if !githubEnvironmentNamePattern.MatchString(results.GitHubEnvironment) {
		return e.New(e.CodeInvalidCatalog, "qualificationResults githubEnvironment must be a lowercase GitHub Environment name")
	}
	if slices.Contains(reservedGitHubEnvironments, results.GitHubEnvironment) {
		return e.New(e.CodeInvalidCatalog, "qualificationResults githubEnvironment must not be another generated job's protected Environment")
	}
	refs := []string{results.BucketRef, results.WorkloadIdentityProviderRef, results.ServiceAccountRef}
	writers := []string{development.WriteCredentialRef, ProductionRegistryWriteCredential}
	if environment.Publication != nil {
		writers = append(writers, environment.Publication.CredentialRef)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		// GITHUB_ names are reserved for the runner's own values, such as its token.
		if !githubSecretRefPattern.MatchString(ref) || strings.HasPrefix(ref, "github:GITHUB_") {
			return e.New(e.CodeInvalidCatalog, "qualificationResults references must be github:<SECRET_NAME> in upper case, outside GITHUB_")
		}
		for _, writer := range writers {
			if strings.EqualFold(ref, writer) {
				return e.New(e.CodeInvalidCatalog, "qualificationResults must not reference a publication writer credential")
			}
		}
		if seen[ref] {
			return e.New(e.CodeInvalidCatalog, "qualificationResults references must be distinct")
		}
		seen[ref] = true
	}
	return nil
}
