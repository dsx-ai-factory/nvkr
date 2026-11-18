// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// BootVerification contains independently chosen expectations. Discovery cannot
// select its own Release, Run, policy, named output set, mapping or limits.
type BootVerification struct {
	Reference string
	// Invocation is the exact aggregate invocation, also on a later retry.
	Invocation  string
	Destination string
	Policy      VerificationPolicy
	Expectation *BootInputExpectation
}

// SelectedBootDescription records selected artifact integrity, original Release
// membership and observed serving availability. It claims neither omitted
// payload verification nor freshness, qualification or boot safety.
type SelectedBootDescription struct {
	Reference         string                            `json:"reference"`
	Invocation        string                            `json:"invocation"`
	ContentSubject    string                            `json:"contentSubject"`
	DefinitionDigest  string                            `json:"definitionDigest"`
	EnvironmentDigest string                            `json:"environmentDigest"`
	PlanDigest        string                            `json:"planDigest"`
	PolicyDigest      string                            `json:"policyDigest"`
	Selection         definition.BootSelection          `json:"selection"`
	Destination       definition.ObjectStoreDestination `json:"destination"`
	Inventory         delivery.StoredObject             `json:"inventory"`
	Assets            []PreparedBootAsset               `json:"assets"`
}

// SelectedBootReceipt cannot be used where a VerifiedRelease or an exact whole
// publication receipt is required. It is issued only after phase cleanup.
type SelectedBootReceipt struct {
	description SelectedBootDescription
	issued      bool
}

func (receipt *SelectedBootReceipt) Description() (SelectedBootDescription, error) {
	if receipt == nil || !receipt.issued {
		return SelectedBootDescription{}, e.New(e.CodeGateUnmet, "selected boot receipt was not issued")
	}
	result := receipt.description
	result.Selection = result.Selection.Clone()
	result.Assets = slices.Clone(result.Assets)
	s3, serving := *result.Destination.S3, *result.Destination.Serving
	result.Destination.S3, result.Destination.Serving = &s3, &serving
	return result, nil
}

func admitBootVerification(options BootVerification) (delivery.BootPhaseRequest, delivery.BootInventoryExpectation, error) {
	var request delivery.BootPhaseRequest
	var expected delivery.BootInventoryExpectation
	if err := admitSelectedBootPolicy(options.Policy); err != nil {
		return request, expected, err
	}
	expectation := options.Expectation
	if expectation == nil || expectation.exact == nil || !expectation.exact.issued || expectation.exact.prepared == nil {
		return request, expected, e.New(e.CodeGateUnmet, "selected boot verification requires its issued Plan expectation")
	}
	var exists bool
	request, exists = expectation.final[options.Destination]
	if !exists {
		return request, expected, e.New(e.CodeVerifyFailed, "selected boot verification names a foreign destination")
	}
	if err := factory.ValidatePublicationInvocation(options.Invocation, options.Invocation); err != nil {
		return request, expected, err
	}
	prepared := expectation.exact.prepared
	expected = delivery.BootInventoryExpectation{RootReference: options.Reference, Invocation: options.Invocation,
		DefinitionDigest: prepared.DefinitionDigest, EnvironmentDigest: prepared.EnvironmentDigest, PlanDigest: prepared.PlanDigest,
		Selection: expectation.selection.Clone(), Destination: request.Destination}
	if err := expected.Validate(); err != nil {
		return request, expected, err
	}
	return request, expected, request.Validate()
}

// VerifyBootSelection retrieves artifacts and intact original proof bytes from
// S3 only. Sigstore trust is independently authenticated through bounded TUF;
// no missing artifact/proof can fall back to OCI or a proof-discovery service.
func (consumer *Consumer) VerifyBootSelection(ctx context.Context, locator delivery.StoredObject, options BootVerification,
	storeOptions delivery.S3ObjectStoreOptions, credential delivery.BootReaderCredential,
) (*SelectedBootReceipt, error) {
	request, expected, err := admitBootVerification(options)
	if err != nil {
		return nil, err
	}
	configured, err := consumer.bootVerifier(options.Policy)
	if err != nil {
		return nil, err
	}
	verifier, ok := configured.(suppliedPublicationVerifier)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "selected consumption requires supplied original proof verification")
	}
	var description SelectedBootDescription
	err = delivery.WithBootPhase(ctx, request, storeOptions, credential, func(ctx context.Context, phase *delivery.BootPhase) error {
		return consumer.withBootTrust(ctx, func(ctx context.Context) (resultErr error) {
			restored, err := phase.RestoreProofs(ctx, locator, expected, consumer.inspection.WorkspaceRoot)
			if err != nil {
				return err
			}
			defer func() { resultErr = errors.Join(resultErr, restored.Close()) }()
			proofs, err := restored.Proofs()
			if err != nil {
				return err
			}
			supplied := &objectProofVerifier{verifier: verifier, proofs: map[string]delivery.RestoredProof{}}
			for _, proof := range proofs {
				if _, exists := supplied.proofs[proof.Subject.Ref]; exists {
					return e.New(e.CodeVerifyFailed, "selected proof repeats an original subject")
				}
				supplied.proofs[proof.Subject.Ref] = proof
			}
			membership, err := authenticateBootMembership(ctx, expected, options.Expectation, options.Policy, supplied,
				func(_ context.Context, reference string, _ []string) (*delivery.SelectedMetadata, error) {
					return restored.Metadata(reference)
				})
			if err != nil {
				return err
			}
			if len(membership.proofs) != len(proofs) {
				return e.New(e.CodeVerifyFailed, "selected proof set contains unused subjects")
			}
			uses, err := restored.Uses()
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(uses, membership.uses) {
				return e.New(e.CodeVerifyFailed, "selected inventory differs from authenticated original membership")
			}
			records, err := restored.StoredAssets()
			if err != nil {
				return err
			}
			available, err := phase.Recheck(ctx, bootMembershipBindings(membership), records)
			if err != nil {
				return err
			}
			description, err = bootReceiptDescription(expected, options.Policy, locator, membership, available)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return &SelectedBootReceipt{description: description, issued: true}, nil
}

func bootMembershipBindings(membership authenticatedBootMembership) []delivery.BootAssetBinding {
	bindings := make([]delivery.BootAssetBinding, len(membership.uses))
	for index, use := range membership.uses {
		bindings[index] = use.Binding
	}
	return bindings
}

func bootReceiptDescription(expected delivery.BootInventoryExpectation, policy VerificationPolicy, locator delivery.StoredObject,
	membership authenticatedBootMembership, available *delivery.BootAvailability,
) (SelectedBootDescription, error) {
	records, err := available.Descriptions()
	if err != nil {
		return SelectedBootDescription{}, err
	}
	if len(records) != len(membership.uses) {
		return SelectedBootDescription{}, e.New(e.CodeVerifyFailed, "availability changed authenticated selected membership")
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return SelectedBootDescription{}, err
	}
	description := SelectedBootDescription{Reference: expected.RootReference, Invocation: expected.Invocation,
		DefinitionDigest: expected.DefinitionDigest, EnvironmentDigest: expected.EnvironmentDigest, PlanDigest: expected.PlanDigest,
		ContentSubject: membership.contentSubject, PolicyDigest: policyDigest, Selection: expected.Selection.Clone(), Destination: expected.Destination, Inventory: locator}
	for index, record := range records {
		use := membership.uses[index]
		if record.Binding != use.Binding || record.URL != use.URL {
			return SelectedBootDescription{}, e.New(e.CodeVerifyFailed, "availability changed a named authenticated raw binding")
		}
		description.Assets = append(description.Assets, PreparedBootAsset{BootStoredAsset: record, PreparedInvocation: use.PreparedInvocation, SignedInvocation: use.SignedInvocation})
	}
	return description, nil
}
