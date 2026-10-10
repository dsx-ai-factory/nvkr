// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle owns the Definition-to-Run-to-Release workflow.
// PrepareFiles freezes a Run attempt for ExecuteLocal; GenerateFiles prepares
// hosted execution, and Consumer.Verify authenticates a published Release under
// consumer policy. This package owns frozen output requirements, attempt state,
// outer publication membership and public projections. Definition owns authored
// admission, factory owns compilation and typed payload observation, and delivery
// owns publication bytes.
package lifecycle

import (
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const (
	DefinitionAPIVersion  = definition.DefinitionAPIVersion
	DefinitionKind        = definition.DefinitionKind
	EnvironmentAPIVersion = definition.EnvironmentAPIVersion
	EnvironmentKind       = definition.EnvironmentKind
	RunSchemaVersion      = "nvkr.dev/run/v4"
	ReleaseSchemaVersion  = "nvkr.dev/release/v4"
)

const (
	OutputExecutable         = definition.OutputExecutable
	OutputOCI                = definition.OutputOCI
	OutputAPT                = definition.OutputAPT
	OutputHelm               = definition.OutputHelm
	OutputDiskImage          = definition.OutputDiskImage
	LaneQualified            = "qualified"
	LanePipeline             = "pipeline-attested"
	LaneUnqualified          = "static-unqualified"
	ScopeArtifactPublication = "artifact-publication"
	ScopeRelease             = "release"
)

// DiskEvidenceRequirement is the minimum disk evidence a consumer accepts.
// Pipeline-attested verification defaults to producer-only evidence. Qualified
// verification requires strict evidence and cannot select the weaker minimum.
type DiskEvidenceRequirement string

const (
	DiskEvidenceStrict          DiskEvidenceRequirement = "strict"
	DiskEvidenceBuildProvenance DiskEvidenceRequirement = sbom.DiskBuildProvenance
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

type Metadata = definition.Metadata
type Definition = definition.Definition
type Catalog = definition.Catalog
type DefinitionOutput = definition.DefinitionOutput
type HelmOutput = definition.HelmOutput
type APTOutput = definition.APTOutput
type Environment = definition.Environment
type SourceAccess = definition.SourceAccess
type Publication = definition.Publication
type Runtime = definition.Runtime
type Signing = definition.Signing

// Artifact is an immutable public output or evidence reference. It always names
// exact observed bytes; required output slots use a separate internal record.
type Artifact = delivery.Output

// Run joins frozen expectations to observations without deriving requirements
// from success. Plans and node reports are inspectable internal records.
type Run struct {
	SchemaVersion     string           `json:"schemaVersion"`
	Attempt           string           `json:"attempt"`
	DefinitionDigest  string           `json:"definitionDigest"`
	EnvironmentDigest string           `json:"environmentDigest"`
	PlanDigest        string           `json:"planDigest"`
	Lane              string           `json:"lane"`
	Status            string           `json:"status"`
	Expected          []requiredOutput `json:"expected"`
	Observed          []Artifact       `json:"observed,omitempty"`
	Failure           *runFailure      `json:"failure,omitempty"`
	ContentSubject    string           `json:"contentSubject,omitempty"`
	// Diagnostic inventories are returned only by this live execution. JSON
	// cannot restore them, and they grant no live or full-SBOM authority.
	diskInventories []factory.DiskInstalledInventoryDescription
}

type requiredOutput struct {
	ImageIndex       *factory.ImageIndexPlan `json:"imageIndex,omitempty"`
	Name             string                  `json:"name"`
	Kind             string                  `json:"kind"`
	ArtifactID       string                  `json:"artifactId"`
	MemberArtifactID string                  `json:"memberArtifactId"`
	BuildDigest      string                  `json:"buildDigest"`
	Components       []string                `json:"components"`
	ComponentResults []string                `json:"componentResults"`
}

type runFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Release is the public, immutable projection returned after lane-specific
// closure verification. Wire manifests and qualified receipts retain their own
// schemas and identities behind this view.
type Release struct {
	SchemaVersion  string     `json:"schemaVersion"`
	Subject        string     `json:"subject"`
	Lane           string     `json:"lane"`
	Scope          string     `json:"scope"`
	Artifacts      []Artifact `json:"artifacts"`
	ContentSubject string     `json:"contentSubject"`
	PolicyDigest   string     `json:"policyDigest"`
}

// VerificationPolicy identifies the exact lane and trust requirements a
// consumer accepts. Empty signer policy never authorizes pipeline evidence.
type VerificationPolicy struct {
	Lane                string                  `json:"lane"`
	Scope               string                  `json:"scope"`
	IdentityRegexp      string                  `json:"identityRegexp,omitempty"`
	Issuer              string                  `json:"issuer,omitempty"`
	MinimumDiskEvidence DiskEvidenceRequirement `json:"minimumDiskEvidence,omitempty"`
}

func (policy VerificationPolicy) Validate() error {
	switch policy.MinimumDiskEvidence {
	case "", DiskEvidenceStrict, DiskEvidenceBuildProvenance:
	default:
		return e.New(e.CodeTrustPolicyMissing, "minimum disk evidence %q is unsupported", policy.MinimumDiskEvidence)
	}
	switch policy.Lane {
	case LaneQualified:
		if policy.Scope != ScopeRelease {
			return e.New(e.CodeTrustPolicyMissing, "qualified verification requires full release scope")
		}
		if policy.MinimumDiskEvidence == DiskEvidenceBuildProvenance {
			return e.New(e.CodeTrustPolicyMissing, "qualified verification cannot accept producer-only disk evidence")
		}
		return nil
	case LanePipeline:
		if policy.Scope != ScopeArtifactPublication && policy.Scope != ScopeRelease {
			return e.New(e.CodeTrustPolicyMissing, "pipeline-attested verification requires an explicit artifact-publication or release scope")
		}
		if strings.TrimSpace(policy.IdentityRegexp) == "" || strings.TrimSpace(policy.Issuer) == "" {
			return e.New(e.CodeTrustPolicyMissing, "pipeline-attested verification requires identity and issuer")
		}
		if _, err := regexp.Compile(policy.IdentityRegexp); err != nil {
			return e.Wrap(e.CodeTrustPolicyMissing, err, "compiling verification identity policy")
		}
		return nil
	default:
		return e.New(e.CodeTrustPolicyMissing, "verification policy lane %q is unsupported", policy.Lane)
	}
}

func (policy VerificationPolicy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	policy.MinimumDiskEvidence = policy.diskEvidenceMinimum()
	return canonical.Digest(canonical.KindVerificationPolicy, policy)
}

func (policy VerificationPolicy) diskEvidenceMinimum() DiskEvidenceRequirement {
	if policy.MinimumDiskEvidence != "" {
		return policy.MinimumDiskEvidence
	}
	if policy.Lane == LaneQualified {
		return DiskEvidenceStrict
	}
	return DiskEvidenceBuildProvenance
}
