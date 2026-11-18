// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// The same original-subject join serves final publication and independent S3
// consumption. Readers own the returned sparse metadata until the phase ends.
type bootMetadataReader func(context.Context, string, []string) (*delivery.SelectedMetadata, error)

type authenticatedBootMembership struct {
	contentSubject string
	uses           []delivery.BootOriginalUse
	publications   []delivery.BootProofPublication
	proofs         []*sign.VerifiedOCIEvidence
}

func authenticateBootMembership(ctx context.Context, expected delivery.BootInventoryExpectation, expectation *BootInputExpectation,
	policy VerificationPolicy, verifier portablePublicationVerifier, read bootMetadataReader,
) (authenticatedBootMembership, error) {
	var result authenticatedBootMembership
	if err := admitSelectedBootPolicy(policy); err != nil {
		return result, err
	}
	if expectation == nil || expectation.exact == nil || !expectation.exact.issued || expectation.exact.prepared == nil || read == nil || verifier == nil {
		return result, e.New(e.CodeGateUnmet, "selected membership requires its issued inputs and original proof reader")
	}
	request, exists := expectation.final[expected.Destination.Name]
	inputs := expectation.exact.prepared
	if !exists || expected.DefinitionDigest != inputs.DefinitionDigest || expected.EnvironmentDigest != inputs.EnvironmentDigest ||
		expected.PlanDigest != inputs.PlanDigest || !reflect.DeepEqual(expected.Selection, expectation.selection) || !reflect.DeepEqual(expected.Destination, request.Destination) {
		return result, e.New(e.CodeVerifyFailed, "selected membership differs from its issued inputs and destination")
	}
	if err := expected.Validate(); err != nil {
		return result, err
	}
	names := selectedBootNames(expectation.selection)
	aggregateOwner, err := read(ctx, expected.RootReference, names)
	if err != nil {
		return result, err
	}
	aggregate, err := aggregateOwner.Description(expected.RootReference)
	if err != nil {
		return result, err
	}
	if aggregate.Metadata.InvocationID != expected.Invocation {
		return result, e.New(e.CodeVerifyFailed, "selected inventory requires the aggregate's exact authenticated invocation")
	}
	seenProofs := map[string]*sign.VerifiedOCIEvidence{}
	proofBytes := map[string]bool{}
	var totalProofBytes int64
	retain := func(reference, signingInvocation string, metadata delivery.ReleaseMetadata) error {
		proof := seenProofs[reference]
		fresh := proof == nil
		if fresh {
			var err error
			proof, err = authenticateOriginalPublicationProof(ctx, verifier, reference, policy)
			if err != nil {
				return err
			}
		}
		consumingInvocation := expected.Invocation
		if reference == expected.RootReference {
			// Assembly fixes inventory identity. Its immutable root may be signed
			// by a later same-run retry; only that authenticated proof can supply
			// the signing bound. Local output proofs still precede assembly.
			var err error
			consumingInvocation, err = bootProofInvocation(proof)
			if err != nil {
				return err
			}
		}
		if err := requirePublicationProvenance(proof, metadata, signingInvocation, consumingInvocation); err != nil {
			return err
		}
		if !fresh {
			return nil
		}
		if err := reserveBootProof(proof, proofBytes, &totalProofBytes); err != nil {
			return err
		}
		seenProofs[reference] = proof
		result.proofs = append(result.proofs, proof)
		return nil
	}
	if err := retain(expected.RootReference, "", aggregate.Metadata); err != nil {
		return result, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return result, err
	}
	// This is a descriptive value solely for the existing pure complete-input
	// join. It is never validated/issued as a Release or a whole-payload receipt.
	logical := Release{Subject: expected.RootReference, PolicyDigest: policyDigest}
	outputs := map[string]delivery.PublicationOutput{}
	for _, output := range aggregate.Outputs {
		logical.Artifacts = append(logical.Artifacts, output.Output)
		outputs[output.Name] = output
	}
	matched, err := matchExactPublicationInputs(logical, aggregate, expectation.exact)
	if err != nil {
		return result, err
	}
	result.contentSubject = matched.ContentSubject
	visit := func(reference string, owner *delivery.SelectedMetadata) error {
		if err := owner.VisitEvidence(ctx, func(ctx context.Context, binding delivery.EvidenceBinding, raw []byte) error {
			return verifier.VerifySBOM(ctx, binding.SubjectDigest, raw)
		}); err != nil {
			return err
		}
		result.publications = append(result.publications, delivery.BootProofPublication{Reference: reference, Metadata: owner})
		return nil
	}
	if err := visit(expected.RootReference, aggregateOwner); err != nil {
		return result, err
	}
	for _, name := range names {
		output, exists := outputs[name]
		if !exists || output.Publication == nil {
			return result, e.New(e.CodeVerifyFailed, "selected output has no original aggregate membership")
		}
		origin := output.Publication
		if repository, _, _ := strings.Cut(origin.Ref, "@"); repository != expectation.repositories[name] {
			return result, e.New(e.CodeVerifyFailed, "selected output root differs from its compiled repository")
		}
		localOwner, err := read(ctx, origin.Ref, []string{name})
		if err != nil {
			return result, err
		}
		local, err := localOwner.Description(origin.Ref)
		if err != nil {
			return result, err
		}
		if err := requireBootOutputInputs(expectation.exact, name, local); err != nil {
			return result, err
		}
		if err := requireMirrorOutput(aggregate, output, local); err != nil {
			return result, err
		}
		if err := requireDiskEvidence(policy, []delivery.Output{output.Output}); err != nil {
			return result, err
		}
		if err := retain(origin.Ref, origin.SignedInvocationID, local.Metadata); err != nil {
			return result, err
		}
		if err := retain(local.Outputs[0].Reference, origin.SignedInvocationID, local.Metadata); err != nil {
			return result, err
		}
		binding, err := originalBootBinding(localOwner, origin.Ref, name, expectation.selection)
		if err != nil {
			return result, err
		}
		// Independently derive the raw description from the aggregate's original
		// controls too. Its repository/root locator is the one permitted change.
		aggregateBinding, err := originalBootBinding(aggregateOwner, expected.RootReference, name, expectation.selection)
		if err != nil {
			return result, err
		}
		aggregateBinding.Publication = binding.Publication
		if aggregateBinding != binding {
			return result, e.New(e.CodeVerifyFailed, "selected raw member differs between original local and aggregate controls")
		}
		url, err := delivery.BootAssetURL(*expected.Destination.Serving, binding)
		if err != nil {
			return result, err
		}
		result.uses = append(result.uses, delivery.BootOriginalUse{Binding: binding, PreparedInvocation: origin.PreparedInvocationID, SignedInvocation: origin.SignedInvocationID, URL: url})
		if err := visit(origin.Ref, localOwner); err != nil {
			return result, err
		}
	}
	slices.SortFunc(result.publications, func(a, b delivery.BootProofPublication) int { return strings.Compare(a.Reference, b.Reference) })
	return result, nil
}

func originalBootBinding(owner *delivery.SelectedMetadata, reference, name string, selection definition.BootSelection) (delivery.BootAssetBinding, error) {
	for _, disk := range selection.Disks {
		if disk.Output == name {
			return owner.BootDiskBinding(reference, disk)
		}
	}
	for _, imager := range selection.Imagers {
		if imager.Output == name {
			return owner.BootImagerBinding(reference, imager)
		}
	}
	return delivery.BootAssetBinding{}, e.New(e.CodeVerifyFailed, "original metadata names an unselected boot output")
}
