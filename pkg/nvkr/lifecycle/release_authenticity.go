// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const AuthenticitySchemaVersion = "nvkr.dev/release-authenticity/v1"

// AuthenticityDescription reports authenticated publisher claims. Artifact
// digests describe signed metadata, not independently checked payload bytes.
// This detached description cannot restore a verification capability.
type AuthenticityDescription struct {
	SchemaVersion            string     `json:"schemaVersion"`
	Subject                  string     `json:"subject"`
	Lane                     string     `json:"lane"`
	Scope                    string     `json:"scope"`
	Artifacts                []Artifact `json:"artifacts"`
	ContentSubject           string     `json:"contentSubject"`
	PolicyDigest             string     `json:"policyDigest"`
	PayloadIntegrityVerified bool       `json:"payloadIntegrityVerified"`
}

// AuthenticatedRelease is a package-issued authenticity result. It has no held
// payloads and cannot satisfy VerifiedRelease or exact-input/export operations.
// Copies share revocation; Close invalidates every copy's Description method.
type AuthenticatedRelease struct {
	state *authenticatedReleaseState
}

type authenticatedReleaseState struct {
	mu           sync.RWMutex
	valid        bool
	description  AuthenticityDescription
	invocationID string
}

func (authenticated *AuthenticatedRelease) Description() (AuthenticityDescription, error) {
	if authenticated == nil || authenticated.state == nil {
		return AuthenticityDescription{}, e.New(e.CodeGateUnmet, "release authenticity result was not issued")
	}
	state := authenticated.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.valid {
		return AuthenticityDescription{}, e.New(e.CodeGateUnmet, "release authenticity result is closed")
	}
	description := state.description
	description.Artifacts = cloneArtifacts(description.Artifacts)
	return description, nil
}

func (authenticated *AuthenticatedRelease) Close() error {
	if authenticated == nil || authenticated.state == nil {
		return nil
	}
	state := authenticated.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.valid = false
	state.description = AuthenticityDescription{}
	state.invocationID = ""
	return nil
}

// Authenticate verifies the root and every named output's signature and
// subject-bound SLSA provenance, the complete metadata membership, and each SBOM
// attestation. It never retrieves artifact payload layers. Qualification,
// revocation, payload availability and payload integrity are not established.
func (consumer *Consumer) Authenticate(ctx context.Context, reference string, policy VerificationPolicy) (*AuthenticatedRelease, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "release authentication requires a context")
	}
	if err := authenticityContext(ctx); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Lane != LanePipeline || policy.Scope != ScopeAuthenticity {
		return nil, e.New(e.CodeEngineCapability, "release authentication requires pipeline-attested authenticity scope")
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return nil, err
	}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) {
		return nil, e.New(e.CodeEngineCapability, "release authentication requires a configured signature adapter")
	}
	if err := consumer.inspection.Registry.Validate(); err != nil {
		return nil, err
	}
	verifier := consumer.verifier
	if verifier == nil {
		var err error
		verifier, err = consumer.verifierFor(policy)
		if err != nil {
			return nil, err
		}
	}
	attester, ok := verifier.(sign.SBOMAttester)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "release authentication requires signed SBOM verification")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := authenticateReleaseSubject(ctx, verifier, reference, policy); err != nil {
		return nil, err
	}
	metadata, err := inspectReleaseMetadata(ctx, reference, consumer.inspection, attester.VerifySBOM)
	if err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0, len(metadata.Outputs))
	for _, output := range metadata.Outputs {
		// APT's Output.Digest is semantic; the signing subject is always the
		// manifest digest in Reference. Aggregate indexes are signed as a whole.
		if err := authenticateReleaseSubject(ctx, verifier, output.Reference, policy); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, output.Output)
	}
	if err := requireDiskEvidence(policy, artifacts); err != nil {
		return nil, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	if err := authenticityContext(ctx); err != nil {
		return nil, err
	}
	return &AuthenticatedRelease{state: &authenticatedReleaseState{valid: true, invocationID: metadata.Metadata.InvocationID, description: AuthenticityDescription{
		SchemaVersion: AuthenticitySchemaVersion, Subject: reference, Lane: policy.Lane, Scope: policy.Scope,
		Artifacts: cloneArtifacts(artifacts), ContentSubject: metadata.Metadata.ContentSubject, PolicyDigest: policyDigest,
		PayloadIntegrityVerified: false,
	}}}, nil
}

func authenticityContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "release authentication canceled")
	}
	return nil
}

func authenticateReleaseSubject(ctx context.Context, verifier sign.OCISigner, reference string, policy VerificationPolicy) error {
	if err := authenticityContext(ctx); err != nil {
		return err
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return err
	}
	subject := sign.Subject{Ref: reference, Digest: referenceDigest(reference)}
	record, err := verifier.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp,
		Issuer: policy.Issuer, RequireAttestation: true})
	if err != nil {
		return err
	}
	if err := authenticityContext(ctx); err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified ||
		record.Ref != subject.Ref || record.Digest != subject.Digest || record.PredicateType != sign.ProvenancePredicateType ||
		record.Identity != policy.IdentityRegexp || record.Issuer != policy.Issuer {
		return e.New(e.CodeSignatureInvalid, "signature and provenance do not authenticate exact release subject %s under the requested policy", reference)
	}
	pattern, err := regexp.Compile(policy.IdentityRegexp)
	if err != nil {
		return e.Wrap(e.CodeTrustPolicyMissing, err, "compiling release identity policy")
	}
	if !pattern.MatchString(record.ObservedIdentity) || record.ObservedIssuer != policy.Issuer {
		return e.New(e.CodeUntrustedSigner, "observed signer for %s does not satisfy the requested identity and issuer policy", reference)
	}
	return nil
}

// sourceInvocation reads only the root metadata retained by Authenticate. A
// detached authenticity description cannot restore this private source binding.
func (authenticated *AuthenticatedRelease) sourceInvocation(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", e.New(e.CodeUsage, "authenticated source lookup requires context")
	}
	if err := authenticityContext(ctx); err != nil {
		return "", err
	}
	if authenticated == nil || authenticated.state == nil {
		return "", e.New(e.CodeGateUnmet, "release authenticity result was not issued")
	}
	state := authenticated.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.valid {
		return "", e.New(e.CodeGateUnmet, "release authenticity result is closed")
	}
	if state.invocationID == "" {
		return "", e.New(e.CodeProvenanceMissing, "authenticated publication root omits its source invocation")
	}
	return state.invocationID, nil
}
