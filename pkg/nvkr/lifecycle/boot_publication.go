// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// PublishBootSelection runs the separate final phase after aggregate signing.
// The preliminary description carries pins only: all original evidence and
// membership are authenticated again and raw S3/HTTPS bytes are read again.
// Success is withheld until original proof publication, inventory readback and
// every owner/session cleanup have completed.
func (consumer *Consumer) PublishBootSelection(ctx context.Context, prepared PreparedBootDescription, options BootVerification,
	storeOptions delivery.S3ObjectStoreOptions, credential delivery.BootReaderCredential,
) (*SelectedBootReceipt, error) {
	return consumer.publishBootSelection(ctx, prepared, options, storeOptions, credential, nil)
}

// RetryBootSelection repeats final authentication and availability checks, then
// reconciles only the exact failed inventory attempt. A known pin is read
// directly; its denial never falls back to a newer version or conditional PUT.
func (consumer *Consumer) RetryBootSelection(ctx context.Context, prepared PreparedBootDescription, options BootVerification,
	storeOptions delivery.S3ObjectStoreOptions, credential delivery.BootReaderCredential, attempt delivery.StoredObject,
) (*SelectedBootReceipt, error) {
	if err := delivery.ValidateBootInventoryAttempt(options.Reference, attempt); err != nil {
		return nil, err
	}
	return consumer.publishBootSelection(ctx, prepared, options, storeOptions, credential, &attempt)
}

func (consumer *Consumer) publishBootSelection(ctx context.Context, prepared PreparedBootDescription, options BootVerification,
	storeOptions delivery.S3ObjectStoreOptions, credential delivery.BootReaderCredential, recovery *delivery.StoredObject,
) (*SelectedBootReceipt, error) {
	request, expected, err := admitBootVerification(options)
	if err != nil {
		return nil, err
	}
	if err := validateBootPreparation(prepared, expected, options.Policy); err != nil {
		return nil, err
	}
	verifier, err := consumer.bootVerifier(options.Policy)
	if err != nil {
		return nil, err
	}
	var description SelectedBootDescription
	var attempted delivery.StoredObject
	err = delivery.WithBootPhase(ctx, request, storeOptions, credential, func(ctx context.Context, phase *delivery.BootPhase) error {
		return consumer.withBootTrust(ctx, func(ctx context.Context) (resultErr error) {
			var owners []*delivery.SelectedMetadata
			defer func() {
				for _, owner := range owners {
					resultErr = errors.Join(resultErr, owner.Close())
				}
			}()
			membership, err := authenticateBootMembership(ctx, expected, options.Expectation, options.Policy, verifier,
				func(ctx context.Context, reference string, names []string) (*delivery.SelectedMetadata, error) {
					owner, err := consumer.pullBootMetadata(ctx, reference, names)
					if err == nil {
						owners = append(owners, owner)
					}
					return owner, err
				})
			if err != nil {
				return err
			}
			records := make([]delivery.BootStoredAsset, len(prepared.Assets))
			for index, asset := range prepared.Assets {
				use := membership.uses[index]
				if asset.Binding != use.Binding || asset.URL != use.URL || asset.PreparedInvocation != use.PreparedInvocation || asset.SignedInvocation != use.SignedInvocation {
					return e.New(e.CodeVerifyFailed, "preliminary boot record differs from final original membership")
				}
				records[index] = asset.BootStoredAsset
			}
			available, err := phase.Recheck(ctx, bootMembershipBindings(membership), records)
			if err != nil {
				return err
			}
			locator, err := phase.PublishProofs(ctx, delivery.BootPublicationRequest{Recovery: recovery, Expected: expected, Availability: available,
				Uses: membership.uses, Publications: membership.publications, Proofs: membership.proofs})
			if err != nil {
				return err
			}
			attempted = locator
			description, err = bootReceiptDescription(expected, options.Policy, locator, membership, available)
			return err
		})
	})
	if err != nil {
		if attempted.Key != "" {
			return nil, &delivery.BootInventoryError{Attempt: attempted, Err: err}
		}
		return nil, err
	}
	return &SelectedBootReceipt{description: description, issued: true}, nil
}

func validateBootPreparation(prepared PreparedBootDescription, expected delivery.BootInventoryExpectation, policy VerificationPolicy) error {
	policyDigest, err := policy.Digest()
	if err != nil {
		return err
	}
	if prepared.DefinitionDigest != expected.DefinitionDigest || prepared.EnvironmentDigest != expected.EnvironmentDigest ||
		prepared.PlanDigest != expected.PlanDigest || prepared.PolicyDigest != policyDigest ||
		!reflect.DeepEqual(prepared.Selection, expected.Selection) || !reflect.DeepEqual(prepared.Destination, expected.Destination) {
		return e.New(e.CodeVerifyFailed, "preliminary boot description differs from admitted inputs, policy or destination")
	}
	if err := factory.ValidatePublicationInvocation(prepared.Invocation, expected.Invocation); err != nil {
		return err
	}
	names := selectedBootNames(expected.Selection)
	if len(names) != len(prepared.Assets) {
		return e.New(e.CodeVerifyFailed, "preliminary boot description changes the selected set")
	}
	for index, asset := range prepared.Assets {
		if asset.Binding.Output != names[index] {
			return e.New(e.CodeVerifyFailed, "preliminary boot description changes named membership or order")
		}
		if err := delivery.ValidatePublicationReference(asset.Binding.Publication); err != nil {
			return err
		}
		url, err := delivery.BootAssetURL(*expected.Destination.Serving, asset.Binding)
		if err != nil {
			return err
		}
		if asset.URL != url {
			return e.New(e.CodeVerifyFailed, "preliminary boot URL differs from admitted mapping")
		}
	}
	return nil
}

func (consumer *Consumer) pullBootMetadata(ctx context.Context, reference string, names []string) (result *delivery.SelectedMetadata, resultErr error) {
	if consumer.bootMetadata != nil {
		return consumer.bootMetadata(ctx, reference, names)
	}
	stage, err := os.MkdirTemp(consumer.inspection.WorkspaceRoot, "nvkr-boot-controls-*")
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "creating original boot metadata workspace")
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "cleaning original boot metadata workspace"))
		}
		if resultErr != nil && result != nil {
			resultErr = errors.Join(resultErr, result.Close())
			result = nil
		}
	}()
	root := filepath.Join(stage, "original")
	if _, err := delivery.PullSelectedMetadata(ctx, reference, root, consumer.inspection.Registry, names); err != nil {
		return nil, err
	}
	return delivery.OpenSelectedMetadata(ctx, root, referenceDigest(reference), names, consumer.inspection.WorkspaceRoot)
}
