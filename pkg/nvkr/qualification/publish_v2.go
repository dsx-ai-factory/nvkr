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

type PolicyV2 struct {
	RequiredSuites   []SuiteRequirement `json:"requiredSuites"`
	OutsideMVPSuites []string           `json:"outsideMvpSuites"`
}

func (p PolicyV2) Validate() error {
	if len(p.RequiredSuites) == 0 || len(p.RequiredSuites) > 256 {
		return e.New(e.CodeTrustPolicyMissing, "v2 requires explicit bounded required-suite policy")
	}
	names := map[string]bool{}
	for _, r := range p.RequiredSuites {
		if err := r.Validate(); err != nil {
			return err
		}
		name := r.Expected.Suite.Name
		if names[name] {
			return e.New(e.CodeTrustPolicyMissing, "duplicate required suite policy")
		}
		names[name] = true
	}
	outside, err := outsideSuites(p.OutsideMVPSuites)
	if err != nil {
		return err
	}
	for _, s := range outside {
		if names[s.Name] {
			return e.New(e.CodeTrustPolicyMissing, "required suite is also outside scope")
		}
	}
	return nil
}

type qualificationAttesterV2 interface {
	AttestQualificationV2(context.Context, sign.Subject, []byte) error
}
type PublisherV2 struct {
	newAttester func(string) (qualificationAttesterV2, error)
}

func NewPublisherV2(ctx context.Context, options Options) (*PublisherV2, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	// Keep the same Linux/pin and source invocation trust as v1.
	if _, err := NewPublisher(options); err != nil {
		return nil, err
	}
	preflight, err := sign.NewPinnedCosignSigner(options.CosignBinary, options.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := preflight.Preflight(ctx); err != nil {
		return nil, err
	}
	return &PublisherV2{newAttester: func(identity string) (qualificationAttesterV2, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest, Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: "https://token.actions.githubusercontent.com", Credential: options.Credential})
	}}, nil
}

type PublicationV2 struct {
	ArtifactChecks    *lifecycle.ArtifactCheckDescription `json:"artifactChecks,omitempty"`
	Evidence          *EvidenceV2                         `json:"evidence,omitempty"`
	EvidenceDigest    string                              `json:"evidenceDigest,omitempty"`
	Verdicts          []Verdict                           `json:"verdicts"`
	PublishedSubjects []string                            `json:"publishedSubjects"`
	Successful        bool                                `json:"successful"`
}

func collectSuitesV2(ctx context.Context, checks lifecycle.ArtifactCheckDescription, policy PolicyV2, receipts []*SuiteReceipt) ([]SuiteEvidenceV2, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if len(receipts) != len(policy.RequiredSuites) {
		return nil, e.New(e.CodeGateUnmet, "required suite receipt set is incomplete or contains extra members")
	}
	accepted := make(map[string]SuiteEvidenceV2, len(receipts))
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
	result := make([]SuiteEvidenceV2, 0, len(accepted))
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
type suiteInvocationV2 interface {
	Description() (qualificationorigin.Description, error)
	RequireSource(qualificationorigin.Description) error
}

func (p *PublisherV2) Publish(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, smoke *SmokeReceipt, policy PolicyV2, receipts []*SuiteReceipt) (*PublicationV2, error) {
	return p.publishReceiptsV2(ctx, checks, invocation, smoke, policy, receipts)
}

func (p *PublisherV2) publishReceiptsV2(ctx context.Context, checks suiteChecks, invocation suiteInvocationV2, smoke *SmokeReceipt, policy PolicyV2, receipts []*SuiteReceipt) (*PublicationV2, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if p == nil || p.newAttester == nil {
		return nil, e.New(e.CodeEngineCapability, "v2 qualification publisher is unavailable")
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
	suites, err := collectSuitesV2(ctx, description, policy, receipts)
	if err != nil {
		return nil, err
	}
	outside, err := outsideSuites(policy.OutsideMVPSuites)
	if err != nil {
		return nil, err
	}
	verdicts, err := collectSmoke(description, qualifier, smoke)
	result := &PublicationV2{ArtifactChecks: &description, Verdicts: verdicts, PublishedSubjects: []string{}}
	if err != nil {
		return result, err
	}
	evidence := EvidenceV2{SchemaVersion: SchemaVersionV2, GateVersion: GateVersionV2, ArtifactChecks: description, Invocation: qualifier, Smoke: verdicts, ExternalSuites: outside, Suites: suites}
	return p.publishEvidenceV2(ctx, evidence, result, func() error {
		if _, err := checks.Description(); err != nil {
			return err
		}
		if err := invocation.RequireSource(description.Origin); err != nil {
			return err
		}
		if _, err := collectSmoke(description, qualifier, smoke); err != nil {
			return err
		}
		_, err := collectSuitesV2(ctx, description, policy, receipts)
		return err
	})
}
func (p *PublisherV2) publishEvidenceV2(ctx context.Context, evidence EvidenceV2, result *PublicationV2, recheck func() error) (*PublicationV2, error) {
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
	attester, err := p.newAttester(evidence.Invocation.BuilderID)
	if err != nil {
		return result, err
	}
	if attester == nil {
		return result, e.New(e.CodeEngineCapability, "v2 signing adapter is unavailable")
	}
	for _, output := range evidence.ArtifactChecks.Outputs {
		if err := suiteContext(ctx); err != nil {
			return result, err
		}
		if err := recheck(); err != nil {
			return result, err
		}
		if err := attester.AttestQualificationV2(ctx, sign.Subject{Ref: output.Reference, Digest: output.Subject}, raw); err != nil {
			return result, e.Wrap(e.CodeGateUnmet, err, "v2 qualification attachment failed; earlier attachments may remain")
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
