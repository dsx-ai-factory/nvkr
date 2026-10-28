// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type releaseMaterializer func(context.Context, string) (*delivery.Publication, error)

// ConsumerOptions configures transport and signature verification once. Each
// verification operation then accepts only the Release reference and Verification Policy.
type ConsumerOptions struct {
	CosignBinary  string
	CosignDigest  string
	Credential    sign.Credential
	Registry      delivery.RegistryOptions
	WorkspaceRoot string
}

type Consumer struct {
	verifier           sign.OCISigner
	verifierFor        func(VerificationPolicy) (sign.OCISigner, error)
	materialize        releaseMaterializer
	materializeChecked qualificationMaterializer
	inspection         InspectionOptions
}

// VerifiedRelease is an opaque, policy-bound live capability. Description is a
// defensive public projection; Close releases its verified local closure.
type VerifiedRelease struct {
	mu          sync.RWMutex
	valid       bool
	release     Release
	publication *delivery.Publication
}

// ExactInputDescription is the defensive projection of a successful exact-input
// check. It identifies both the authenticated publication and the exact authored
// and compiled inputs matched against it.
type ExactInputDescription struct {
	ReleaseSubject    string `json:"releaseSubject"`
	PolicyDigest      string `json:"policyDigest"`
	DefinitionDigest  string `json:"definitionDigest"`
	EnvironmentDigest string `json:"environmentDigest"`
	PlanDigest        string `json:"planDigest"`
	ContentSubject    string `json:"contentSubject"`
}

// ExactInputExpectation is an opaque, package-issued snapshot prepared before
// registry or signature work. Callers cannot construct an accepted expectation
// from digest strings or mutable Plan fields.
type ExactInputExpectation struct {
	issued   bool
	prepared *Run
	outputs  []factory.LifecycleOutputPlan
}

// ExactInputReceipt is an opaque result issued only after a verified publication
// has matched a package-issued ExactInputExpectation.
type ExactInputReceipt struct {
	issued      bool
	owner       *VerifiedRelease
	description ExactInputDescription
}

func NewConsumer(options ConsumerOptions) (*Consumer, error) {
	root := options.WorkspaceRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nvkr", "verified-releases"))
	}
	return &Consumer{inspection: InspectionOptions{WorkspaceRoot: options.WorkspaceRoot, Registry: options.Registry}, verifierFor: func(policy VerificationPolicy) (sign.OCISigner, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary,
			BinaryDigest: options.CosignDigest, Identity: policy.IdentityRegexp, Issuer: policy.Issuer, Credential: options.Credential})
	}, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		return delivery.PullPublication(ctx, reference, root, options.Registry)
	}, materializeChecked: func(ctx context.Context, reference string) (qualificationPublication, error) {
		return delivery.PullCheckedPublication(ctx, reference, root, options.Registry)
	}}, nil
}

func (verified *VerifiedRelease) Description() (Release, error) {
	if verified == nil {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	description := verified.release
	description.Artifacts = cloneArtifacts(verified.release.Artifacts)
	return description, nil
}

// PrepareExactInputs validates and freezes the consumer's Definition,
// Environment, and canonical compiled Plan bytes before any registry or signature work.
func PrepareExactInputs(definition Definition, environment Environment, plan *factory.Plan) (*ExactInputExpectation, error) {
	prepared, err := PrepareRun(definition, environment, plan, "consumer-verification")
	if err != nil {
		return nil, err
	}
	frozenOutputs := make([]factory.LifecycleOutputPlan, len(plan.LifecycleOutputs))
	for index, output := range plan.LifecycleOutputs {
		frozenOutputs[index] = output
		frozenOutputs[index].Components = append([]string(nil), output.Components...)
		frozenOutputs[index].ImageIndex = output.ImageIndex.Clone()
	}
	return &ExactInputExpectation{issued: true, prepared: prepared, outputs: frozenOutputs}, nil
}

// Description returns the immutable identities authenticated by this receipt.
func (receipt *ExactInputReceipt) Description() (ExactInputDescription, error) {
	if receipt == nil || !receipt.issued {
		return ExactInputDescription{}, e.New(e.CodeGateUnmet, "exact-input receipt was not issued")
	}
	return receipt.description, nil
}

// RequireExactInputs checks that this already verified publication realizes a
// package-prepared exact-input expectation. It grants no stronger release scope
// or evidence lane.
func (verified *VerifiedRelease) RequireExactInputs(expectation *ExactInputExpectation) (*ExactInputReceipt, error) {
	if verified == nil {
		return nil, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid || verified.publication == nil {
		return nil, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	if expectation == nil || !expectation.issued || expectation.prepared == nil || len(expectation.outputs) == 0 {
		return nil, e.New(e.CodeGateUnmet, "exact-input expectation was not issued")
	}
	description, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		return nil, err
	}
	matched, err := matchExactPublicationInputs(verified.release, description, expectation)
	if err != nil {
		return nil, err
	}
	return &ExactInputReceipt{issued: true, owner: verified, description: matched}, nil
}

// Both retained Release exports and qualification's checked owner must realize
// the same frozen Definition, Environment, Plan and complete output identity.
func matchExactPublicationInputs(release Release, description delivery.PublicationDescription, expectation *ExactInputExpectation) (ExactInputDescription, error) {
	if description.Metadata.DefinitionDigest != expectation.prepared.DefinitionDigest ||
		description.Metadata.EnvironmentDigest != expectation.prepared.EnvironmentDigest ||
		description.Metadata.PlanDigest != expectation.prepared.PlanDigest {
		return ExactInputDescription{}, e.New(e.CodeVerifyFailed,
			"verified publication does not bind the supplied Definition, Environment, and Plan")
	}
	observed, err := bindObserved(expectation.prepared, release.Artifacts)
	if err != nil {
		return ExactInputDescription{}, err
	}
	if observed.Status != RunComplete {
		return ExactInputDescription{}, e.New(e.CodeReleaseIncomplete, "verified publication omits a required compiled output")
	}
	bound, err := bindReleaseContentOutputs(expectation.outputs, observed)
	if err != nil {
		return ExactInputDescription{}, err
	}
	if bound.ContentSubject != description.Metadata.ContentSubject {
		return ExactInputDescription{}, e.New(e.CodeVerifyFailed, "verified publication content does not realize the exact compiled output set")
	}
	return ExactInputDescription{
		ReleaseSubject: release.Subject, PolicyDigest: release.PolicyDigest,
		DefinitionDigest:  expectation.prepared.DefinitionDigest,
		EnvironmentDigest: expectation.prepared.EnvironmentDigest, PlanDigest: expectation.prepared.PlanDigest,
		ContentSubject: bound.ContentSubject,
	}, nil
}

// VerifyExactInputs authenticates one publication and immediately narrows it to
// a previously prepared exact-input expectation. A failed narrowing closes the
// verified publication before returning.
func (consumer *Consumer) VerifyExactInputs(ctx context.Context, reference string, policy VerificationPolicy,
	expectation *ExactInputExpectation,
) (*VerifiedRelease, *ExactInputReceipt, error) {
	if expectation == nil || !expectation.issued || expectation.prepared == nil || len(expectation.outputs) == 0 {
		return nil, nil, e.New(e.CodeGateUnmet, "exact-input expectation was not issued")
	}
	verified, err := consumer.Verify(ctx, reference, policy)
	if err != nil {
		return nil, nil, err
	}
	receipt, err := verified.RequireExactInputs(expectation)
	if err != nil {
		if closeErr := verified.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, nil, err
	}
	return verified, receipt, nil
}

func (verified *VerifiedRelease) Close() error {
	if verified == nil {
		return nil
	}
	verified.mu.Lock()
	defer verified.mu.Unlock()
	if !verified.valid {
		return nil
	}
	verified.valid = false
	return verified.publication.Close()
}

// Verify consumes one pinned reference under one explicit policy. The currently
// implemented positive scope authenticates a pipeline-produced publication root
// signature and SLSA provenance attestation, then independently pulls and checks
// every reachable descriptor. Full release and qualified scopes remain closed.
func (consumer *Consumer) Verify(ctx context.Context, reference string, policy VerificationPolicy) (_ *VerifiedRelease, resultErr error) {
	if err := consumer.authenticatePublication(ctx, reference, policy, consumer != nil && consumer.materialize != nil); err != nil {
		return nil, err
	}
	publication, err := consumer.materialize(ctx, reference)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			if closeErr := publication.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, closeErr)
			}
		}
	}()
	description, err := describeVerifiedPublication(reference, policy, publication)
	if err != nil {
		return nil, err
	}
	failed = false
	return &VerifiedRelease{valid: true, release: description, publication: publication}, nil
}

// authenticatePublication enforces the shared policy and exact root signature
// before either full retained materialization or sequential qualification work.
func (consumer *Consumer) authenticatePublication(ctx context.Context, reference string, policy VerificationPolicy, materializerReady bool) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "release verification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "release verification canceled")
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return err
	}
	digest := referenceDigest(reference)
	subject := sign.Subject{Ref: reference, Digest: digest}
	if err := subject.Validate(); err != nil {
		return err
	}
	if policy.Lane == LaneQualified {
		return e.New(e.CodeEngineCapability, "qualified Release verification requires authenticated discovery, clearance, trusted-time, status, and publication-commit adapters")
	}
	if policy.Scope == ScopeRelease {
		return e.New(e.CodeEngineCapability, "pipeline-attested full Release verification requires qualification and lifecycle-state adapters")
	}
	if policy.Scope != ScopeArtifactPublication {
		return e.New(e.CodeEngineCapability, "full payload verification requires artifact-publication scope; use Authenticate for authenticity scope")
	}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) || !materializerReady {
		return e.New(e.CodeEngineCapability, "artifact-publication verification requires configured signature and registry adapters")
	}
	verifier := consumer.verifier
	if verifier == nil {
		configured, configureErr := consumer.verifierFor(policy)
		if configureErr != nil {
			return configureErr
		}
		verifier = configured
	}
	record, err := verifier.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp,
		Issuer: policy.Issuer, RequireAttestation: true})
	if err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Ref != reference || record.Digest != digest {
		return e.New(e.CodeSignatureInvalid, "publication signature and provenance do not authenticate the exact requested subject")
	}
	identityPattern, err := regexp.Compile(policy.IdentityRegexp)
	if err != nil {
		return e.Wrap(e.CodeTrustPolicyMissing, err, "compiling release identity policy")
	}
	if !identityPattern.MatchString(record.ObservedIdentity) || record.ObservedIssuer != policy.Issuer {
		return e.New(e.CodeUntrustedSigner, "observed release signer does not satisfy the requested identity and issuer policy")
	}
	return nil
}

type publicationVerificationView interface {
	Artifacts(string) ([]delivery.Output, string, error)
	RequireAttestedEvidence() error
}

func describeVerifiedPublication(reference string, policy VerificationPolicy, publication publicationVerificationView) (Release, error) {
	artifacts, contentSubject, err := publication.Artifacts(reference)
	if err != nil {
		return Release{}, err
	}
	if err := requireDiskEvidence(policy, artifacts); err != nil {
		return Release{}, err
	}
	if err := publication.RequireAttestedEvidence(); err != nil {
		return Release{}, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return Release{}, err
	}
	description := Release{SchemaVersion: ReleaseSchemaVersion, Subject: reference, Lane: policy.Lane,
		Scope: policy.Scope, Artifacts: artifacts, ContentSubject: contentSubject, PolicyDigest: policyDigest}
	if err := description.validate(); err != nil {
		return Release{}, err
	}
	return description, nil
}

func requireDiskEvidence(policy VerificationPolicy, artifacts []delivery.Output) error {
	minimum := policy.diskEvidenceMinimum()
	for _, artifact := range artifacts {
		if artifact.Kind != OutputDiskImage {
			continue
		}
		if artifact.Disk == nil {
			return e.New(e.CodeVerifyFailed, "disk artifact %q has no evidence binding", artifact.Name)
		}
		switch artifact.Disk.EvidenceMode {
		case "": // Strict inspected evidence is stronger than either policy floor.
		case string(DiskEvidenceBuildProvenance):
			if minimum != DiskEvidenceBuildProvenance {
				return e.New(e.CodeGateUnmet, "disk artifact %q has producer-only evidence, below consumer minimum %q", artifact.Name, minimum)
			}
		default:
			return e.New(e.CodeVerifyFailed, "disk artifact %q has unsupported evidence mode %q", artifact.Name, artifact.Disk.EvidenceMode)
		}
	}
	return nil
}

// InspectReleaseLayout returns exact Artifact descriptions from an already
// materialized publication closure. It authenticates no release lane.
func InspectReleaseLayout(ctx context.Context, reference, layoutPath string) ([]Artifact, error) {
	digest := referenceDigest(reference)
	if err := (sign.Subject{Ref: reference, Digest: digest}).Validate(); err != nil {
		return nil, err
	}
	publication, err := delivery.OpenPublication(ctx, layoutPath, digest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = publication.Close() }()
	artifacts, _, err := publication.Artifacts(reference)
	return artifacts, err
}

func (release Release) validate() error {
	if release.SchemaVersion != ReleaseSchemaVersion || release.Lane != LanePipeline || release.Scope != ScopeArtifactPublication ||
		!canonical.IsDigest(release.PolicyDigest) || !canonical.IsDigest(release.ContentSubject) || len(release.Artifacts) == 0 {
		return e.New(e.CodeGateUnmet, "Release does not contain a supported policy-bound verified closure")
	}
	digest := referenceDigest(release.Subject)
	if err := (sign.Subject{Ref: release.Subject, Digest: digest}).Validate(); err != nil {
		return err
	}
	previous := ""
	for _, artifact := range release.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		key := artifact.Name + "\x00" + artifact.Kind
		if key <= previous {
			return e.New(e.CodeVerifyFailed, "Release artifacts must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func referenceDigest(reference string) string {
	if strings.Count(reference, "@") != 1 {
		return ""
	}
	return reference[strings.LastIndexByte(reference, '@')+1:]
}
