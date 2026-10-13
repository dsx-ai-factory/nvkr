// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"regexp"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const PolicySchemaV3 = "nvkr.dev/qualification-policy/v2"

// PolicyV3 extends independently selected native policy without changing its
// authenticated worker contract. Its native pin binds the complete v1 policy.
type PolicyV3 struct {
	SchemaVersion      string             `json:"schemaVersion"`
	NativePolicyDigest string             `json:"nativePolicyDigest"`
	RequiredSuites     []SuiteRequirement `json:"requiredSuites"`
}

func (p PolicyV3) Validate() error {
	if p.SchemaVersion != PolicySchemaV3 || !canonical.IsDigest(p.NativePolicyDigest) {
		return e.New(e.CodeTrustPolicyMissing, "required-suite policy requires its exact schema and full native policy digest")
	}
	if len(p.RequiredSuites) == 0 || len(p.RequiredSuites) > 256 {
		return e.New(e.CodeTrustPolicyMissing, "v3 requires explicit bounded required-suite policy")
	}
	names := map[string]bool{}
	for _, requirement := range p.RequiredSuites {
		if err := requirement.Validate(); err != nil {
			return err
		}
		name := requirement.Expected.Suite.Name
		if names[name] {
			return e.New(e.CodeTrustPolicyMissing, "duplicate required suite policy")
		}
		names[name] = true
	}
	return nil
}

func (p PolicyV3) normalized() (PolicyV3, error) {
	if err := p.Validate(); err != nil {
		return PolicyV3{}, err
	}
	raw, err := canonical.Marshal(p)
	if err != nil {
		return PolicyV3{}, err
	}
	var frozen PolicyV3
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return PolicyV3{}, err
	}
	sort.Slice(frozen.RequiredSuites, func(i, j int) bool {
		return frozen.RequiredSuites[i].Expected.Suite.Name < frozen.RequiredSuites[j].Expected.Suite.Name
	})
	return frozen, nil
}

func (p PolicyV3) Digest() (string, error) {
	frozen, err := p.normalized()
	if err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualificationPolicy, frozen)
}

func matchNativePolicyV3(policy PolicyV3, native lifecycle.QualificationPolicy, cosignDigest string) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := matchPublicationPolicy(Policy{QualificationPolicyDigest: policy.NativePolicyDigest, OutsideMVPSuites: native.OutsideMVPSuites}, native, cosignDigest); err != nil {
		return err
	}
	for _, requirement := range policy.RequiredSuites {
		for _, outside := range native.OutsideMVPSuites {
			if outside == requirement.Expected.Suite.Name {
				return e.New(e.CodeTrustPolicyMissing, "required suite is also outside native policy scope")
			}
		}
	}
	return nil
}

type qualificationAttesterV3 interface {
	AttestQualificationV3(context.Context, sign.Subject, []byte) error
}
type PublisherV3 struct {
	cosignDigest string
	newAttester  func(string) (qualificationAttesterV3, error)
}

func NewPublisherV3(ctx context.Context, options Options) (*PublisherV3, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	// Keep the native v2 Linux/pin and source invocation trust.
	if _, err := NewPublisher(options); err != nil {
		return nil, err
	}
	if _, err := sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest, Identity: "^qualification-admission$", Issuer: "https://token.actions.githubusercontent.com", Credential: options.Credential}); err != nil {
		return nil, err
	}
	preflight, err := sign.NewPinnedCosignSigner(options.CosignBinary, options.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := preflight.Preflight(ctx); err != nil {
		return nil, err
	}
	return &PublisherV3{cosignDigest: options.CosignDigest, newAttester: func(identity string) (qualificationAttesterV3, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest, Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: "https://token.actions.githubusercontent.com", Credential: options.Credential})
	}}, nil
}

type PublicationV3 struct {
	ArtifactChecks    *lifecycle.ArtifactCheckDescription `json:"artifactChecks,omitempty"`
	Evidence          *EvidenceV3                         `json:"evidence,omitempty"`
	EvidenceDigest    string                              `json:"evidenceDigest,omitempty"`
	Verdicts          []Verdict                           `json:"verdicts"`
	PublishedSubjects []string                            `json:"publishedSubjects"`
	Successful        bool                                `json:"successful"`
}

func collectSuitesV3(ctx context.Context, checks lifecycle.ArtifactCheckDescription, policy PolicyV3, receipts []*SuiteReceipt) ([]SuiteEvidenceV3, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if len(receipts) != len(policy.RequiredSuites) {
		return nil, e.New(e.CodeGateUnmet, "required suite receipt set is incomplete or contains extra members")
	}
	accepted := make(map[string]SuiteEvidenceV3, len(receipts))
	for _, receipt := range receipts {
		suite, err := receipt.require(ctx, checks)
		if err != nil {
			return nil, err
		}
		if _, ok := accepted[suite.Name]; ok {
			return nil, e.New(e.CodeGateUnmet, "duplicate suite receipt")
		}
		accepted[suite.Name] = suite
	}
	result := make([]SuiteEvidenceV3, 0, len(accepted))
	for _, r := range policy.RequiredSuites {
		suite, ok := accepted[r.Expected.Suite.Name]
		if !ok {
			return nil, e.New(e.CodeGateUnmet, "required suite receipt missing")
		}
		want, err := canonical.Marshal(r)
		if err != nil {
			return nil, err
		}
		got, err := canonical.Marshal(suite.Requirement)
		if err != nil {
			return nil, err
		}
		if string(want) != string(got) {
			return nil, e.New(e.CodeGateUnmet, "suite receipt differs from independently selected policy")
		}
		result = append(result, suite)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// The public boundary accepts only package-issued production capabilities. Its
// private delegate keeps the same admission and recheck closure testable without
// introducing a caller-supplied authority constructor.
type suiteInvocationV3 interface {
	Description() (qualificationorigin.Description, error)
	RequireSource(qualificationorigin.Description) error
}

func (p *PublisherV3) Publish(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, smoke *SmokeReceipt, policy PolicyV3, receipts []*SuiteReceipt) (*PublicationV3, error) {
	return p.publishReceiptsV3(ctx, checks, invocation, smoke, policy, receipts)
}

func (p *PublisherV3) publishReceiptsV3(ctx context.Context, checks suiteChecks, invocation suiteInvocationV3, smoke suiteSmokeAuthorityV3, policy PolicyV3, receipts []*SuiteReceipt) (*PublicationV3, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if p == nil || p.newAttester == nil {
		return nil, e.New(e.CodeEngineCapability, "v3 qualification publisher is unavailable")
	}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	qualifier, err := invocation.Description()
	if err != nil {
		return nil, err
	}
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, err
	}
	suites, err := collectSuitesV3(ctx, description, policy, receipts)
	if err != nil {
		return nil, err
	}
	verdicts, nativeSmoke, err := collectSmokeV3(description, qualifier, smoke, policy.NativePolicyDigest)
	result := &PublicationV3{ArtifactChecks: &description, Verdicts: verdicts, PublishedSubjects: []string{}}
	if err != nil {
		return result, err
	}
	if err := matchNativePolicyV3(policy, nativeSmoke.Policy, p.cosignDigest); err != nil {
		return result, err
	}
	outside, err := outsideSuites(nativeSmoke.Policy.OutsideMVPSuites)
	if err != nil {
		return result, err
	}
	frozenPolicy, err := policy.normalized()
	if err != nil {
		return result, err
	}
	policyDigest, err := frozenPolicy.Digest()
	if err != nil {
		return result, err
	}
	evidence := EvidenceV3{ToolSmoke: nativeSmoke, Policy: frozenPolicy, PolicyDigest: policyDigest, SchemaVersion: SchemaVersionV3, GateVersion: GateVersionV3, ArtifactChecks: description, Invocation: qualifier, Smoke: verdicts, ExternalSuites: outside, Suites: suites}
	return p.publishEvidenceV3(ctx, evidence, result, func() error {
		if _, err := checks.Description(); err != nil {
			return err
		}
		if err := invocation.RequireSource(description.Origin); err != nil {
			return err
		}
		if _, _, err := collectSmokeV3(description, qualifier, smoke, policy.NativePolicyDigest); err != nil {
			return err
		}
		_, err := collectSuitesV3(ctx, description, policy, receipts)
		return err
	})
}
func (p *PublisherV3) publishEvidenceV3(ctx context.Context, evidence EvidenceV3, result *PublicationV3, recheck func() error) (*PublicationV3, error) {
	raw, err := evidence.Canonical()
	if err != nil {
		return result, err
	}
	identity, err := evidence.Digest()
	if err != nil {
		return result, err
	}
	result.Evidence = &evidence
	result.EvidenceDigest = identity
	// Revalidate every authority before constructing any signing adapter.
	if err := suiteContext(ctx); err != nil {
		return result, err
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := suiteContext(ctx); err != nil {
		return result, err
	}
	attester, err := p.newAttester(evidence.Invocation.BuilderID)
	if err != nil {
		return result, err
	}
	if attester == nil {
		return result, e.New(e.CodeEngineCapability, "v3 signing adapter is unavailable")
	}
	for _, output := range evidence.ArtifactChecks.Outputs {
		if err := suiteContext(ctx); err != nil {
			return result, err
		}
		if err := recheck(); err != nil {
			return result, err
		}
		if err := suiteContext(ctx); err != nil {
			return result, err
		}
		if err := attester.AttestQualificationV3(ctx, sign.Subject{Ref: output.Reference, Digest: output.Subject}, raw); err != nil {
			return result, e.Wrap(e.CodeGateUnmet, err, "v3 qualification attachment failed; earlier attachments may remain")
		}
		result.PublishedSubjects = append(result.PublishedSubjects, output.Reference)
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := suiteContext(ctx); err != nil {
		return result, err
	}
	result.Successful = true
	return result, nil
}

// This seam is private; the public publisher accepts only lifecycle-issued
// native receipts. Descriptions cannot restore authority through public options.
type suiteSmokeAuthorityV3 interface {
	Require(lifecycle.ArtifactCheckDescription, qualificationorigin.Description, string) error
	Description() (lifecycle.ToolSmokeDescription, error)
}

func collectSmokeV3(checks lifecycle.ArtifactCheckDescription, invocation qualificationorigin.Description, receipt suiteSmokeAuthorityV3, digest string) ([]Verdict, lifecycle.ToolSmokeDescription, error) {
	if receipt == nil {
		return nil, lifecycle.ToolSmokeDescription{}, e.New(e.CodeGateUnmet, "authenticated native smoke is missing")
	}
	if err := receipt.Require(checks, invocation, digest); err != nil {
		return nil, lifecycle.ToolSmokeDescription{}, err
	}
	description, err := receipt.Description()
	if err != nil {
		return nil, lifecycle.ToolSmokeDescription{}, err
	}
	verdicts, err := smokeVerdicts(description)
	return verdicts, description, err
}
