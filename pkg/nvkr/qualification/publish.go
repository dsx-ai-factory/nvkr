// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"regexp"
	"runtime"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// SmokeReceipt reserves the exact-subject, invocation-bound M3-B integration.
// No public constructor accepts pass booleans or caller-authored result files.
// Until B implements its authenticated execution owner, required tool smoke
// cannot produce this receipt and qualification must fail closed.
type SmokeReceipt struct {
	artifactChecks string
	invocation     qualificationorigin.Description
	verdicts       []Verdict
	closed         *atomic.Bool
}

func (s *SmokeReceipt) Close() error {
	if s != nil && s.closed != nil {
		s.closed.Store(true)
	}
	return nil
}

// Policy reserves external suites without treating them as successful tests.
// A policy requiring any external suite remains unavailable until its signed,
// exact-subject result authenticator is implemented.
type Policy struct {
	OutsideMVPSuites []string
	RequiredSuites   []string
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
	newAttester func(string) (qualificationAttester, error)
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
	return &Publisher{newAttester: func(identity string) (qualificationAttester, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{
			Binary: options.CosignBinary, BinaryDigest: options.CosignDigest, Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: "https://token.actions.githubusercontent.com", Credential: options.Credential,
		})
	}}, nil
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
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, err
	}
	suites, err := outsideSuites(policy.OutsideMVPSuites)
	if err != nil {
		return nil, err
	}
	verdicts, err := collectSmoke(description, qualifier, smoke)
	result := &Publication{ArtifactChecks: &description, Verdicts: verdicts, PublishedSubjects: []string{}}
	if err != nil {
		return result, err
	}
	evidence := Evidence{SchemaVersion: SchemaVersion, GateVersion: GateVersion, ArtifactChecks: description, Invocation: qualifier, Smoke: verdicts, ExternalSuites: suites}
	return p.publishEvidence(ctx, evidence, result, func() error {
		if _, err := checks.Description(); err != nil {
			return err
		}
		if err := invocation.RequireSource(description.Origin); err != nil {
			return err
		}
		_, err := collectSmoke(description, qualifier, smoke)
		return err
	})
}

func collectSmoke(checks lifecycle.ArtifactCheckDescription, invocation qualificationorigin.Description, receipt *SmokeReceipt) ([]Verdict, error) {
	var authenticated []Verdict
	if receipt != nil {
		raw, err := canonical.Marshal(checks)
		if err != nil {
			return nil, err
		}
		if receipt.closed == nil || receipt.closed.Load() || receipt.artifactChecks != string(raw) || receipt.invocation != invocation {
			return nil, e.New(e.CodeGateUnmet, "smoke receipt does not bind the exact artifact checks and qualification invocation")
		}
		authenticated = receipt.verdicts
		if len(authenticated) != len(checks.Outputs) {
			return nil, e.New(e.CodeReleaseIncomplete, "smoke receipt omits or adds selected outputs")
		}
	}
	verdicts := make([]Verdict, 0, len(checks.Outputs))
	var failures []error
	for i, output := range checks.Outputs {
		verdict := Verdict{Output: output.Name, Subject: output.Subject, Check: "delivered-tool-version"}
		required, err := requiresSmoke(output.Kind)
		switch {
		case err != nil:
			verdict.Status = "fail"
			verdict.Reason = "M3-B applicability is unavailable"
			failures = append(failures, err)
		case !required:
			verdict.Status = "not-applicable"
			verdict.Reason = "non-tool artifact format"
			if len(authenticated) != 0 && authenticated[i] != verdict {
				verdict.Status = "fail"
				verdict.Reason = "smoke receipt contradicts non-tool applicability"
				failures = append(failures, e.New(e.CodeGateUnmet, "output %q has contradictory smoke evidence", output.Name))
			}
		case len(authenticated) == 0:
			verdict.Status = "fail"
			verdict.Reason = "authenticated M3-B smoke result is missing"
			failures = append(failures, e.New(e.CodeGateUnmet, "output %q has no authenticated delivered-tool smoke", output.Name))
		default:
			verdict = authenticated[i]
			if verdict.Output != output.Name || verdict.Subject != output.Subject || verdict.Check != "delivered-tool-version" || verdict.Status != "pass" {
				verdict.Status = "fail"
				failures = append(failures, e.New(e.CodeGateUnmet, "output %q has failing or mismatched smoke", output.Name))
			}
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts, errors.Join(failures...)
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
