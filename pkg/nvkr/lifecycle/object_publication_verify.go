// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// ObjectPublicationVerification contains caller expectations. Neither the store
// locator nor its inventory may select a signer, original subject, Run or budget.
type ObjectPublicationVerification struct {
	Reference   string
	Invocation  string
	Policy      VerificationPolicy
	Expectation *ExactInputExpectation
	Limits      delivery.MirrorLimits
}

type suppliedPublicationVerifier interface {
	portablePublicationVerifier
	VerifyBundles(context.Context, sign.Subject, sign.VerifyPolicy, []byte, []byte) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error)
}

// VerifyObjectPublication independently restores the original closure and
// authenticates its original signatures, provenance, SBOMs and complete named
// output membership. Signature discovery and payload retrieval never consult an
// OCI registry. The returned existing capability retains artifact-publication
// scope; an S3 copy does not establish boot, installation or qualification.
func (consumer *Consumer) VerifyObjectPublication(ctx context.Context, store delivery.ObjectStore, locator delivery.StoredObject,
	options ObjectPublicationVerification,
) (result *VerifiedRelease, exact *ExactInputReceipt, resultErr error) {
	if ctx == nil || store == nil || options.Invocation == "" {
		return nil, nil, e.New(e.CodeUsage, "object publication verification requires context, store and originating Run")
	}
	if err := options.Limits.Validate(); err != nil {
		return nil, nil, err
	}
	if err := options.Policy.Validate(); err != nil {
		return nil, nil, err
	}
	if options.Policy.Lane != LanePipeline || options.Policy.Scope != ScopeArtifactPublication {
		return nil, nil, e.New(e.CodeEngineCapability, "object publication consumption requires pipeline-attested artifact-publication scope")
	}
	if options.Expectation == nil || !options.Expectation.issued || options.Expectation.prepared == nil || len(options.Expectation.outputs) == 0 {
		return nil, nil, e.New(e.CodeGateUnmet, "object publication consumption requires a prepared exact-input expectation")
	}
	if err := delivery.ValidatePublicationReference(options.Reference); err != nil {
		return nil, nil, err
	}
	configured, err := consumer.portableVerifier(options.Policy)
	if err != nil {
		return nil, nil, err
	}
	verifier, ok := configured.(suppliedPublicationVerifier)
	if !ok {
		return nil, nil, e.New(e.CodeEngineCapability, "object consumption requires original supplied-bundle verification")
	}
	stage, err := os.MkdirTemp(consumer.inspection.WorkspaceRoot, "nvkr-object-consumer-*")
	if err != nil {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "creating object publication workspace")
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "cleaning object publication workspace"))
		}
		if resultErr != nil && result != nil {
			resultErr = errors.Join(resultErr, result.Close())
			result, exact = nil, nil
		}
	}()
	restored, err := delivery.RestoreMirror(ctx, store, locator, filepath.Join(stage, "restored"), options.Limits)
	if err != nil {
		return nil, nil, err
	}
	if restored.RootReference != options.Reference {
		return nil, nil, e.New(e.CodeVerifyFailed, "object inventory identifies a different original publication")
	}
	proofs := map[string]delivery.RestoredProof{}
	for _, proof := range restored.Proofs {
		if _, exists := proofs[proof.Subject.Ref]; exists {
			return nil, nil, e.New(e.CodeVerifyFailed, "object publication repeats an original proof subject")
		}
		proofs[proof.Subject.Ref] = proof
	}
	publications := map[string]delivery.RestoredPublication{}
	for _, publication := range restored.Publications {
		if _, exists := publications[publication.Reference]; exists {
			return nil, nil, e.New(e.CodeVerifyFailed, "object publication repeats an original publication")
		}
		publications[publication.Reference] = publication
	}
	offline := &Consumer{verifier: &objectProofVerifier{verifier: verifier, proofs: proofs}, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		publication, exists := publications[reference]
		if !exists || publication.RootDigest != referenceDigest(reference) {
			return nil, e.New(e.CodeVerifyFailed, "object inventory omits the exact original publication")
		}
		return delivery.OpenPublication(ctx, publication.Path, publication.RootDigest)
	}}
	verified, receipt, err := offline.VerifyExactInputs(ctx, options.Reference, options.Policy, options.Expectation)
	if err != nil {
		return nil, nil, err
	}
	request, owners, err := offline.mirrorSources(ctx, verified, receipt, options.Invocation, options.Policy, offline.verifier.(portablePublicationVerifier))
	for _, owner := range owners {
		err = errors.Join(err, owner.Close())
	}
	if err == nil && len(request.Publications) != len(publications) {
		err = e.New(e.CodeVerifyFailed, "object inventory includes publications outside the authenticated aggregate")
	}
	if err == nil && len(request.Proofs) != len(proofs) {
		err = e.New(e.CodeVerifyFailed, "object inventory includes proofs outside the authenticated aggregate")
	}
	if err != nil {
		return nil, nil, errors.Join(err, verified.Close())
	}
	return verified, receipt, nil
}

// This adapter has no registry fallback. All evidence bytes must be present in
// the restored inventory and verified under the caller's original policy.
type objectProofVerifier struct {
	verifier suppliedPublicationVerifier
	proofs   map[string]delivery.RestoredProof
}

func (verifier *objectProofVerifier) VerifyAndRetain(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error) {
	proof, exists := verifier.proofs[subject.Ref]
	if !exists || proof.Subject != subject {
		return sign.VerificationRecord{}, nil, e.New(e.CodeProvenanceMissing, "object publication omits the exact original proof")
	}
	return verifier.verifier.VerifyBundles(ctx, subject, policy, proof.Signature, proof.Provenance)
}

func (verifier *objectProofVerifier) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	record, _, err := verifier.VerifyAndRetain(ctx, subject, policy)
	return record, err
}

func (*objectProofVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, e.New(e.CodeEngineCapability, "object publication consumer cannot sign")
}

func (verifier *objectProofVerifier) VerifySBOM(ctx context.Context, digest string, bundle []byte) error {
	return verifier.verifier.VerifySBOM(ctx, digest, bundle)
}

func (*objectProofVerifier) AttestSBOM(context.Context, string, []byte) ([]byte, error) {
	return nil, e.New(e.CodeEngineCapability, "object publication consumer cannot attest")
}
