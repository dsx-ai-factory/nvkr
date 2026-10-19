// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"regexp"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// SmokeReceipt aliases the lifecycle-owned authenticated complete smoke receipt.
// Only hosted aggregation can issue it; decoded reports cannot authorize signing.
type SmokeReceipt = lifecycle.ToolSmokeReceipt

// Policy reserves external suites without treating them as successful tests.
// A policy requiring any external suite remains unavailable until its signed,
// exact-subject result authenticator is implemented.
type Policy struct {
	QualificationPolicyDigest string
	OutsideMVPSuites          []string
	RequiredSuites            []string
}

type Options struct {
	CosignBinary string
	CosignDigest string
	Credential   sign.Credential
}

// Publication is a diagnostic outcome, never release authorization. On an
// operational failure, earlier attachments may remain and Successful is false.
type Publication struct {
	ArtifactChecks    *lifecycle.ArtifactCheckDescription `json:"artifactChecks,omitempty"`
	EvidenceDigest    string                              `json:"evidenceDigest,omitempty"`
	Evidence          *Evidence                           `json:"evidence,omitempty"`
	Verdicts          []Verdict                           `json:"verdicts"`
	PublishedSubjects []string                            `json:"publishedSubjects"`
	Successful        bool                                `json:"successful"`
}

type Publisher struct {
	development  bool
	cosignDigest string
	newAttester  func(string) (qualificationAttester, error)
}
type qualificationAttester interface {
	AttestQualification(context.Context, sign.Subject, []byte) error
}

// NewPublisher admits the Linux pinned signer before callers request provider or
// artifact work. There is no production fake/stub selector.
func NewPublisher(options Options) (*Publisher, error) {
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "qualification signing requires Linux pinned cosign execution")
	}
	if !canonical.IsDigest(options.CosignDigest) || options.CosignBinary == "" {
		return nil, e.New(e.CodeUnpinnedInput, "qualification requires an exact cosign executable pin")
	}
	return &Publisher{cosignDigest: options.CosignDigest, newAttester: func(identity string) (qualificationAttester, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{
			Binary: options.CosignBinary, BinaryDigest: options.CosignDigest, Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: "https://token.actions.githubusercontent.com", Credential: options.Credential,
		})
	}}, nil
}

// NewDevelopmentPublisher selects the separate development predicate transport.
func NewDevelopmentPublisher(options Options) (*Publisher, error) {
	publisher, err := NewPublisher(options)
	if err != nil {
		return nil, err
	}
	production := publisher.newAttester
	publisher.development = true
	publisher.newAttester = func(identity string) (qualificationAttester, error) {
		signer, err := production(identity)
		if err != nil {
			return nil, err
		}
		concrete, ok := signer.(*sign.CosignOCISigner)
		if !ok {
			return nil, e.New(e.CodeEngineCapability, "development signing adapter is unavailable")
		}
		return developmentAttester{concrete}, nil
	}
	return publisher, nil
}

type developmentAttester struct{ signer *sign.CosignOCISigner }

func (a developmentAttester) AttestQualification(ctx context.Context, subject sign.Subject, predicate []byte) error {
	return a.signer.AttestDevelopmentQualification(ctx, subject, predicate)
}

// Publish admits all gate and smoke verdicts before the first signing operation.
// It consumes only the opaque A and invocation receipts. Operational failures
// never claim rollback; the returned diagnostics retain the exact earlier set.
func (p *Publisher) Publish(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, smoke *SmokeReceipt, policy Policy) (*Publication, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification canceled")
	}
	if p == nil || p.newAttester == nil {
		return nil, e.New(e.CodeEngineCapability, "qualification publisher is unavailable")
	}
	if len(policy.RequiredSuites) > 0 {
		return nil, e.New(e.CodeEngineCapability, "required external suite result authentication is unavailable")
	}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	qualifier, err := invocation.Description()
	if err != nil {
		return nil, err
	}
	if p.development != (description.Origin.Branch != "") || p.development != (qualifier.Branch != "") {
		return nil, e.New(e.CodeUntrustedSigner, "qualification publisher rejects a different evidence lane")
	}
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, err
	}
	if !canonical.IsDigest(policy.QualificationPolicyDigest) {
		return nil, e.New(e.CodeTrustPolicyMissing, "qualification requires the independently selected full policy digest")
	}
	verdicts, smokeDescription, err := collectSmoke(description, qualifier, smoke, policy.QualificationPolicyDigest)
	result := &Publication{ArtifactChecks: &description, Verdicts: verdicts, PublishedSubjects: []string{}}
	if err != nil {
		return result, err
	}
	if err := matchPublicationPolicy(policy, smokeDescription.Policy, p.cosignDigest); err != nil {
		return result, err
	}
	suites, err := outsideSuites(policy.OutsideMVPSuites)
	if err != nil {
		return result, err
	}
	evidence := Evidence{SchemaVersion: SchemaVersion, GateVersion: GateVersion, ArtifactChecks: description, Invocation: qualifier, Smoke: verdicts, ToolSmoke: smokeDescription, ExternalSuites: suites}
	if p.development {
		evidence.SchemaVersion = DevelopmentSchemaVersion
		evidence.GateVersion = DevelopmentGateVersion
	}
	return p.publishEvidence(ctx, evidence, result, func() error {
		if _, err := checks.Description(); err != nil {
			return err
		}
		if err := invocation.RequireSource(description.Origin); err != nil {
			return err
		}
		return smoke.Require(description, qualifier, policy.QualificationPolicyDigest)
	})
}

func matchPublicationPolicy(selected Policy, policy lifecycle.QualificationPolicy, cosignDigest string) error {
	if len(policy.RequiredSuites) > 0 || len(selected.RequiredSuites) > 0 {
		return e.New(e.CodeEngineCapability, "required external suite result authentication is unavailable")
	}
	digest, err := policy.Digest()
	if err != nil {
		return err
	}
	if digest != selected.QualificationPolicyDigest {
		return e.New(e.CodeTrustPolicyMissing, "qualification publisher policy differs from authenticated smoke policy")
	}
	expected, err := outsideSuites(policy.OutsideMVPSuites)
	if err != nil {
		return err
	}
	actual, err := outsideSuites(selected.OutsideMVPSuites)
	if err != nil {
		return err
	}
	a, err := canonical.Marshal(expected)
	if err != nil {
		return err
	}
	b, err := canonical.Marshal(actual)
	if err != nil {
		return err
	}
	if string(a) != string(b) {
		return e.New(e.CodeTrustPolicyMissing, "qualification suite selection differs from independent policy")
	}
	pin, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return err
	}
	if cosignDigest != pin {
		return e.New(e.CodeUnpinnedInput, "qualification signer differs from independent policy executable pin")
	}
	return nil
}

func collectSmoke(checks lifecycle.ArtifactCheckDescription, invocation qualificationorigin.Description, receipt *SmokeReceipt, policyDigest string) ([]Verdict, lifecycle.ToolSmokeDescription, error) {
	if err := receipt.Require(checks, invocation, policyDigest); err != nil {
		verdicts := make([]Verdict, 0, len(checks.Outputs))
		for _, output := range checks.Outputs {
			verdicts = append(verdicts, Verdict{Output: output.Name, Subject: output.Subject, Check: "delivered-tool-version", Status: "fail", Reason: "authenticated complete tool smoke is missing"})
		}
		return verdicts, lifecycle.ToolSmokeDescription{}, err
	}
	description, err := receipt.Description()
	if err != nil {
		return nil, lifecycle.ToolSmokeDescription{}, err
	}
	verdicts, err := smokeVerdicts(description)
	return verdicts, description, err
}

// smokeVerdicts is a diagnostic projection of a structurally complete claim.
// Only collectSmoke's live receipt check permits its use by the public publisher.
func smokeVerdicts(description lifecycle.ToolSmokeDescription) ([]Verdict, error) {
	if err := description.Validate(); err != nil {
		return nil, err
	}
	reasons := map[string]string{}
	for _, rule := range description.Policy.Smoke.NonToolOutputs {
		reasons[rule.Output] = rule.Reason
	}
	verdicts := make([]Verdict, 0, len(description.ArtifactChecks.Outputs))
	for _, output := range description.ArtifactChecks.Outputs {
		verdict := Verdict{Output: output.Name, Subject: output.Subject, Check: "delivered-tool-version"}
		switch output.Kind {
		case lifecycle.OutputExecutable:
			verdict.Status = "pass"
		case lifecycle.OutputHelm, lifecycle.OutputDiskImage:
			verdict.Status = "not-applicable"
			verdict.Reason = "non-tool artifact format"
		case lifecycle.OutputOCI, lifecycle.OutputAPT:
			verdict.Status = "not-applicable"
			verdict.Reason = reasons[output.Name]
		default:
			return nil, e.New(e.CodeEngineCapability, "unsupported smoke output kind %q", output.Kind)
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts, nil
}

func (p *Publisher) publishEvidence(ctx context.Context, evidence Evidence, result *Publication, recheck func() error) (*Publication, error) {
	raw, err := evidence.Canonical()
	if err != nil {
		return result, err
	}
	digest, err := evidence.Digest()
	if err != nil {
		return result, err
	}
	result.EvidenceDigest = digest
	result.Evidence = &evidence
	if err := recheck(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "qualification canceled before signer admission")
	}
	attester, err := p.newAttester(evidence.Invocation.BuilderID)
	if err != nil {
		return result, err
	}
	if attester == nil {
		return result, e.New(e.CodeEngineCapability, "qualification signing adapter is unavailable")
	}
	for _, output := range evidence.ArtifactChecks.Outputs {
		if err := ctx.Err(); err != nil {
			return result, e.Wrap(e.CodeTimeout, err, "qualification interrupted; earlier attachments may remain")
		}
		if err := recheck(); err != nil {
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, e.Wrap(e.CodeTimeout, err, "qualification canceled before attachment")
		}
		if err := attester.AttestQualification(ctx, sign.Subject{Ref: output.Reference, Digest: output.Subject}, raw); err != nil {
			return result, e.Wrap(e.CodeGateUnmet, err, "qualification attachment failed for %q; earlier attachments may remain", output.Name)
		}
		result.PublishedSubjects = append(result.PublishedSubjects, output.Reference)
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "qualification interrupted after attachment")
	}
	result.Successful = true
	return result, nil
}
