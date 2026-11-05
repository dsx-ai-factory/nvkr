// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type portablePublicationVerifier interface {
	sign.OCISigner
	sign.SBOMAttester
	VerifyAndRetain(context.Context, sign.Subject, sign.VerifyPolicy) (sign.VerificationRecord, *sign.VerifiedOCIEvidence, error)
}

// MirrorExactInputs copies a complete, authenticated publication and its original
// proofs. The original signatures remain the authority: the returned inventory
// locates bytes and does not grant release, freshness or destination authority.
// A failed copy may leave immutable blobs, but never a successful Run result.
func (consumer *Consumer) MirrorExactInputs(ctx context.Context, reference string, policy VerificationPolicy,
	expectation *ExactInputExpectation, invocation string, store delivery.ObjectStore, limits delivery.MirrorLimits,
) (_ delivery.StoredObject, resultErr error) {
	if ctx == nil || store == nil || strings.TrimSpace(invocation) == "" {
		return delivery.StoredObject{}, e.New(e.CodeUsage, "object publication requires context, store and exact invocation")
	}
	if err := limits.Validate(); err != nil {
		return delivery.StoredObject{}, err
	}
	verifier, err := consumer.portableVerifier(policy)
	if err != nil {
		return delivery.StoredObject{}, err
	}
	// Reuse the full existing consumer gate, not a second interpretation of Plan
	// membership. Pin this operation to the same configured verifier instance.
	checked := *consumer
	checked.verifier, checked.verifierFor = verifier, nil
	verified, receipt, err := checked.VerifyExactInputs(ctx, reference, policy, expectation)
	if err != nil {
		return delivery.StoredObject{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, verified.Close()) }()
	request, owners, err := checked.mirrorSources(ctx, verified, receipt, invocation, policy, verifier)
	defer func() {
		for _, owner := range owners {
			resultErr = errors.Join(resultErr, owner.Close())
		}
	}()
	if err != nil {
		return delivery.StoredObject{}, err
	}
	request.Limits = limits
	return delivery.MirrorPublications(ctx, store, request)
}

func (consumer *Consumer) portableVerifier(policy VerificationPolicy) (portablePublicationVerifier, error) {
	if consumer == nil {
		return nil, e.New(e.CodeEngineCapability, "object publication requires the original publication verifier")
	}
	verifier := consumer.verifier
	if verifier == nil && consumer.verifierFor != nil {
		var err error
		verifier, err = consumer.verifierFor(policy)
		if err != nil {
			return nil, err
		}
	}
	portable, ok := verifier.(portablePublicationVerifier)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "object publication requires portable original signature and provenance verification")
	}
	return portable, nil
}

// mirrorSources retains every output-local root in addition to the aggregate.
// It is also used after object-store restoration, so a consumer independently
// repeats the aggregate/local descriptor join instead of trusting the publisher.
func (consumer *Consumer) mirrorSources(ctx context.Context, verified *VerifiedRelease, receipt *ExactInputReceipt,
	invocation string, policy VerificationPolicy, verifier portablePublicationVerifier,
) (request delivery.MirrorRequest, owners []*delivery.Publication, resultErr error) {
	if receipt == nil || !receipt.issued || receipt.owner != verified {
		return request, nil, e.New(e.CodeGateUnmet, "object publication requires its exact-input receipt")
	}
	description, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		return request, nil, err
	}
	if err := factory.ValidatePublicationInvocation(description.Metadata.InvocationID, invocation); err != nil {
		return request, nil, err
	}
	request.RootReference = verified.release.Subject
	seen := map[string]*sign.VerifiedOCIEvidence{}
	proof := func(reference, signingInvocation string, metadata delivery.ReleaseMetadata) error {
		if retained := seen[reference]; retained != nil {
			return requirePublicationProvenance(retained, metadata, signingInvocation, invocation)
		}
		retained, err := retainPublicationProof(ctx, verifier, reference, policy, metadata, signingInvocation, invocation)
		if err != nil {
			return err
		}
		seen[reference] = retained
		request.Proofs = append(request.Proofs, retained)
		return nil
	}
	visit := func(reference, signingInvocation string, publication *delivery.Publication, local delivery.PublicationDescription) error {
		if err := proof(reference, signingInvocation, local.Metadata); err != nil {
			return err
		}
		for _, output := range local.Outputs {
			outputInvocation := signingInvocation
			outputMetadata := local.Metadata
			if output.Publication != nil {
				outputInvocation = output.Publication.SignedInvocationID
				outputMetadata.InvocationID = output.Publication.PreparedInvocationID
			}
			if err := proof(output.Reference, outputInvocation, outputMetadata); err != nil {
				return err
			}
		}
		if err := publication.VisitEvidence(ctx, func(ctx context.Context, binding delivery.EvidenceBinding, raw []byte) error {
			return verifier.VerifySBOM(ctx, binding.SubjectDigest, raw)
		}); err != nil {
			return err
		}
		request.Publications = append(request.Publications, delivery.MirrorPublication{Reference: reference, Publication: publication})
		return nil
	}
	if err := visit(verified.release.Subject, "", verified.publication, description); err != nil {
		return request, owners, err
	}
	for _, output := range description.Outputs {
		origin := output.Publication
		if origin == nil {
			continue // A monolithic publication already retains the whole closure.
		}
		if consumer.materialize == nil {
			return request, owners, e.New(e.CodeEngineCapability, "object publication has no original output materializer")
		}
		publication, err := consumer.materialize(ctx, origin.Ref)
		if err != nil {
			return request, owners, err
		}
		owners = append(owners, publication)
		local, err := publication.Description(origin.Ref)
		if err != nil {
			return request, owners, err
		}
		if err := requireMirrorOutput(description, output, local); err != nil {
			return request, owners, err
		}
		if err := visit(origin.Ref, origin.SignedInvocationID, publication, local); err != nil {
			return request, owners, err
		}
	}
	slices.SortFunc(request.Publications, func(a, b delivery.MirrorPublication) int { return strings.Compare(a.Reference, b.Reference) })
	return request, owners, nil
}

func requireMirrorOutput(aggregate delivery.PublicationDescription, output delivery.PublicationOutput, local delivery.PublicationDescription) error {
	origin := output.Publication
	if origin == nil || len(local.Outputs) != 1 || local.Reference != origin.Ref ||
		local.Metadata.InvocationID != origin.PreparedInvocationID ||
		local.Metadata.DefinitionDigest != aggregate.Metadata.DefinitionDigest ||
		local.Metadata.EnvironmentDigest != aggregate.Metadata.EnvironmentDigest ||
		local.Metadata.PlanDigest != aggregate.Metadata.PlanDigest ||
		!local.Metadata.Composition.Equal(aggregate.Metadata.Composition) {
		return e.New(e.CodeVerifyFailed, "original output publication differs from authenticated aggregate inputs or invocation")
	}
	got, want := local.Outputs[0], output
	// Assembly can relocate the repository and add the original publication
	// locator. Every other field, including the actual wrapper descriptor and
	// distinct raw/semantic digest, must be identical.
	got.Publication, want.Publication = nil, nil
	got.Reference, want.Reference = "", ""
	if !reflect.DeepEqual(got, want) {
		return e.New(e.CodeVerifyFailed, "original output descriptor or membership differs from authenticated aggregate")
	}
	return nil
}

func retainPublicationProof(ctx context.Context, verifier portablePublicationVerifier, reference string, policy VerificationPolicy,
	metadata delivery.ReleaseMetadata, signingInvocation, consumingInvocation string,
) (*sign.VerifiedOCIEvidence, error) {
	subject := sign.Subject{Ref: reference, Digest: referenceDigest(reference)}
	record, proof, err := verifier.VerifyAndRetain(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp, Issuer: policy.Issuer, RequireAttestation: true})
	if err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if proof == nil || record.DryRun || !record.SignatureVerified || !record.AttestationVerified ||
		record.Ref != subject.Ref || record.Digest != subject.Digest || record.Identity != policy.IdentityRegexp || record.Issuer != policy.Issuer {
		return nil, e.New(e.CodeSignatureInvalid, "original proof does not authenticate the exact subject and external policy")
	}
	retained, _, _, err := proof.Bundles()
	if err != nil || retained != subject {
		return nil, e.New(e.CodeSignatureInvalid, "retained original proof belongs to a different subject")
	}
	if err := requirePublicationProvenance(proof, metadata, signingInvocation, consumingInvocation); err != nil {
		return nil, err
	}
	return proof, nil
}

func requirePublicationProvenance(proof *sign.VerifiedOCIEvidence, metadata delivery.ReleaseMetadata, signingInvocation, consumingInvocation string) error {
	raw, err := proof.ProvenancePredicate()
	if err != nil {
		return err
	}
	var predicate provenance.Predicate
	if err := json.Unmarshal(raw, &predicate); err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding authenticated original provenance")
	}
	if err := provenance.ValidateProduction(predicate); err != nil {
		return err
	}
	parameters := predicate.BuildDefinition.ExternalParameters
	actualInvocation := predicate.RunDetails.Metadata["invocationId"]
	if (signingInvocation != "" && actualInvocation != signingInvocation) ||
		parameters["definitionDigest"] != metadata.DefinitionDigest || parameters["environmentDigest"] != metadata.EnvironmentDigest ||
		parameters["planDigest"] != metadata.PlanDigest || predicate.BuildDefinition.InternalParameters["signingMode"] != provenance.SigningModeCosign {
		return e.New(e.CodeVerifyFailed, "original provenance does not bind exact inputs and signing invocation")
	}
	if err := factory.ValidatePublicationInvocation(metadata.InvocationID, actualInvocation); err != nil {
		return err
	}
	if err := factory.ValidatePublicationInvocation(actualInvocation, consumingInvocation); err != nil {
		return err
	}
	return nil
}
