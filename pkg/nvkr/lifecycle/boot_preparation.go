// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// BootInputExpectation owns exact compiled inputs and independently selected
// destinations. It cannot be restored from digest strings or inventory JSON.
type BootInputExpectation struct {
	exact        *ExactInputExpectation
	selection    definition.BootSelection
	repositories map[string]string
	preparation  map[string]delivery.BootPhaseRequest
	final        map[string]delivery.BootPhaseRequest
}

func PrepareBootInputs(plan *factory.Plan) (*BootInputExpectation, error) {
	if plan == nil || plan.BootPublication == nil {
		return nil, e.New(e.CodeEngineCapability, "selected boot inputs require their versioned complete Plan")
	}
	exact, err := PrepareExactInputs(plan.Definition, plan.Environment, plan)
	if err != nil {
		return nil, err
	}
	expectation := &BootInputExpectation{exact: exact, selection: plan.Environment.Publication.BootSelection.Clone(),
		repositories: map[string]string{}, preparation: map[string]delivery.BootPhaseRequest{}, final: map[string]delivery.BootPhaseRequest{}}
	for _, name := range selectedBootNames(expectation.selection) {
		target, exists := plan.Targets["output-"+name]
		if !exists || target.Kind != factory.TargetOCI {
			return nil, e.New(e.CodePipelineDrift, "selected output lacks its compiled OCI repository")
		}
		expectation.repositories[name] = target.Ref
	}
	for _, destination := range plan.BootPublication.Destinations {
		prepare, _, err := factory.BootPhaseForNode(plan, "prepare-boot/"+destination.Name)
		if err != nil {
			return nil, err
		}
		final, _, err := factory.BootPhaseForNode(plan, "publish-boot/"+destination.Name)
		if err != nil {
			return nil, err
		}
		expectation.preparation[destination.Name], expectation.final[destination.Name] = prepare, final
	}
	return expectation, nil
}

func selectedBootNames(selection definition.BootSelection) []string {
	names := make([]string, 0, len(selection.Disks)+len(selection.Imagers))
	for _, disk := range selection.Disks {
		names = append(names, disk.Output)
	}
	for _, imager := range selection.Imagers {
		names = append(names, imager.Output)
	}
	slices.Sort(names)
	return names
}

type PreparedBootAsset struct {
	delivery.BootStoredAsset
	PreparedInvocation string `json:"preparedInvocation"`
	SignedInvocation   string `json:"signedInvocation"`
}

// PreparedBootDescription has deliberately no aggregate subject or release
// content identity. These records remain transport expectations after copying.
type PreparedBootDescription struct {
	DefinitionDigest  string                            `json:"definitionDigest"`
	EnvironmentDigest string                            `json:"environmentDigest"`
	PlanDigest        string                            `json:"planDigest"`
	PolicyDigest      string                            `json:"policyDigest"`
	Invocation        string                            `json:"invocation"`
	Selection         definition.BootSelection          `json:"selection"`
	Destination       definition.ObjectStoreDestination `json:"destination"`
	Assets            []PreparedBootAsset               `json:"assets"`
}

// PreparedBootAssets is valid only in its preparation phase. It authenticates
// selected output bytes, inputs, policy and observed availability; it cannot
// authenticate membership in an aggregate that has not yet been assembled.
type PreparedBootAssets struct {
	availability *delivery.BootAvailability
	description  PreparedBootDescription
}

func (receipt *PreparedBootAssets) Description() (PreparedBootDescription, error) {
	if receipt == nil || receipt.availability == nil {
		return PreparedBootDescription{}, e.New(e.CodeGateUnmet, "prepared boot receipt was not issued")
	}
	if _, err := receipt.availability.Descriptions(); err != nil {
		return PreparedBootDescription{}, err
	}
	result := receipt.description
	result.Selection = result.Selection.Clone()
	result.Assets = slices.Clone(result.Assets)
	s3, serving := *result.Destination.S3, *result.Destination.Serving
	result.Destination.S3, result.Destination.Serving = &s3, &serving
	return result, nil
}

// WithPreparedBootAssets authenticates original output-local publications,
// copies selected raw bytes, and supplies a live receipt to the callback. The
// returned description is released only after cleanup and cannot restore that
// receipt. Callers supply the validated invocation and fresh runtime access.
func (consumer *Consumer) WithPreparedBootAssets(ctx context.Context, expectation *BootInputExpectation, destination string,
	references map[string]string, invocation string, policy VerificationPolicy, options delivery.S3ObjectStoreOptions,
	credential delivery.BootReaderCredential, use func(context.Context, *PreparedBootAssets) error,
) (PreparedBootDescription, error) {
	if use == nil {
		return PreparedBootDescription{}, e.New(e.CodeUsage, "boot preparation requires a receipt consumer")
	}
	var completion PreparedBootDescription
	err := consumer.prepareBootAssets(ctx, expectation, destination, references, invocation, policy, options, credential,
		func(ctx context.Context, receipt *PreparedBootAssets) error {
			if err := use(ctx, receipt); err != nil {
				return err
			}
			var err error
			completion, err = receipt.Description()
			return err
		})
	if err != nil {
		return PreparedBootDescription{}, err
	}
	// Only a successful phase including cleanup releases the descriptive carrier.
	// The live receipt is already revoked; this value cannot recreate it.
	return completion, nil
}

func (consumer *Consumer) prepareBootAssets(ctx context.Context, expectation *BootInputExpectation, destination string,
	references map[string]string, invocation string, policy VerificationPolicy, options delivery.S3ObjectStoreOptions,
	credential delivery.BootReaderCredential, use func(context.Context, *PreparedBootAssets) error,
) error {
	if err := admitSelectedBootPolicy(policy); err != nil {
		return err
	}
	if expectation == nil || expectation.exact == nil || !expectation.exact.issued || expectation.exact.prepared == nil || use == nil {
		return e.New(e.CodeGateUnmet, "boot preparation requires its issued input expectation and receipt consumer")
	}
	request, exists := expectation.preparation[destination]
	if !exists {
		return e.New(e.CodeVerifyFailed, "boot preparation names a foreign destination")
	}
	if err := factory.ValidatePublicationInvocation(invocation, invocation); err != nil {
		return err
	}
	if len(references) != len(expectation.repositories) {
		return e.New(e.CodeVerifyFailed, "boot preparation omits or adds selected output references")
	}
	references = maps.Clone(references)
	for name, repository := range expectation.repositories {
		reference := references[name]
		if err := delivery.ValidatePublicationReference(reference); err != nil {
			return err
		}
		if actual, _, _ := strings.Cut(reference, "@"); actual != repository {
			return e.New(e.CodeVerifyFailed, "selected output reference differs from compiled repository")
		}
	}
	if consumer == nil || consumer.materialize == nil {
		return e.New(e.CodeEngineCapability, "boot preparation requires the original publication reader")
	}
	verifier, err := consumer.bootVerifier(policy)
	if err != nil {
		return err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return err
	}
	return delivery.WithBootPhase(ctx, request, options, credential, func(ctx context.Context, phase *delivery.BootPhase) error {
		return consumer.withBootTrust(ctx, func(ctx context.Context) (resultErr error) {
			var publications []*delivery.Publication
			var assets []*delivery.BootAsset
			defer func() {
				for _, asset := range assets {
					resultErr = errors.Join(resultErr, asset.Close())
				}
				for _, publication := range publications {
					resultErr = errors.Join(resultErr, publication.Close())
				}
			}()
			prepared := expectation.exact.prepared
			description := PreparedBootDescription{DefinitionDigest: prepared.DefinitionDigest, EnvironmentDigest: prepared.EnvironmentDigest,
				PlanDigest: prepared.PlanDigest, PolicyDigest: policyDigest, Invocation: invocation, Selection: expectation.selection.Clone(), Destination: request.Destination}
			proofs := map[string]bool{}
			var proofBytes int64
			for _, name := range selectedBootNames(expectation.selection) {
				reference := references[name]
				publication, err := consumer.materialize(ctx, reference)
				if err != nil {
					return err
				}
				publications = append(publications, publication)
				local, err := publication.Description(reference)
				if err != nil {
					return err
				}
				if err := requireBootOutputInputs(expectation.exact, name, local); err != nil {
					return err
				}
				if err := requireDiskEvidence(policy, []delivery.Output{local.Outputs[0].Output}); err != nil {
					return err
				}
				proof, err := retainPublicationProof(ctx, verifier, reference, policy, local.Metadata, "", invocation)
				if err != nil {
					return err
				}
				if err := reserveBootProof(proof, proofs, &proofBytes); err != nil {
					return err
				}
				signedInvocation, err := bootProofInvocation(proof)
				if err != nil {
					return err
				}
				for _, output := range local.Outputs {
					proof, err := retainPublicationProof(ctx, verifier, output.Reference, policy, local.Metadata, signedInvocation, invocation)
					if err != nil {
						return err
					}
					if err := reserveBootProof(proof, proofs, &proofBytes); err != nil {
						return err
					}
				}
				if err := publication.VisitEvidence(ctx, func(ctx context.Context, binding delivery.EvidenceBinding, raw []byte) error {
					return verifier.VerifySBOM(ctx, binding.SubjectDigest, raw)
				}); err != nil {
					return err
				}
				var asset *delivery.BootAsset
				for _, disk := range expectation.selection.Disks {
					if disk.Output == name {
						asset, err = publication.OpenBootDisk(ctx, reference, disk)
					}
				}
				for _, imager := range expectation.selection.Imagers {
					if imager.Output == name {
						asset, err = publication.OpenBootImager(ctx, reference, imager)
					}
				}
				if err != nil {
					return err
				}
				if asset == nil {
					return e.New(e.CodeVerifyFailed, "selected output has no exact raw owner")
				}
				assets = append(assets, asset)
				description.Assets = append(description.Assets, PreparedBootAsset{BootStoredAsset: delivery.BootStoredAsset{Binding: delivery.BootAssetBinding{Output: name}}, PreparedInvocation: local.Metadata.InvocationID, SignedInvocation: signedInvocation})
			}
			availability, err := phase.Copy(ctx, assets)
			if err != nil {
				return err
			}
			records, err := availability.Descriptions()
			if err != nil {
				return err
			}
			if len(records) != len(description.Assets) {
				return e.New(e.CodeVerifyFailed, "boot preparation changed the complete named asset set")
			}
			for index, record := range records {
				if record.Binding.Output != description.Assets[index].Binding.Output {
					return e.New(e.CodeVerifyFailed, "boot preparation changed the output/invocation join")
				}
				description.Assets[index].BootStoredAsset = record
			}
			return use(ctx, &PreparedBootAssets{availability: availability, description: description})
		})
	})
}

func (consumer *Consumer) bootVerifier(policy VerificationPolicy) (portablePublicationVerifier, error) {
	verifier, err := consumer.portableVerifier(policy)
	if err != nil {
		return nil, err
	}
	bounded, ok := verifier.(interface{ AdmitBoundedVerification() error })
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "selected boot verification requires bounded keyless trust acquisition")
	}
	if err := bounded.AdmitBoundedVerification(); err != nil {
		return nil, err
	}
	return verifier, nil
}

func (consumer *Consumer) withBootTrust(ctx context.Context, work func(context.Context) error) error {
	if consumer.bootTrust != nil {
		return consumer.bootTrust(ctx, work)
	}
	return sign.WithBoundedVerification(ctx, work)
}

func requireBootOutputInputs(expectation *ExactInputExpectation, name string, local delivery.PublicationDescription) error {
	if expectation == nil || !expectation.issued || expectation.prepared == nil {
		return e.New(e.CodeGateUnmet, "boot output requires its issued input expectation")
	}
	prepared := expectation.prepared
	if local.Metadata.DefinitionDigest != prepared.DefinitionDigest || local.Metadata.EnvironmentDigest != prepared.EnvironmentDigest ||
		local.Metadata.PlanDigest != prepared.PlanDigest || !local.Metadata.Composition.Equal(prepared.Composition) ||
		len(local.Outputs) != 1 || local.Outputs[0].Name != name || local.Outputs[0].Publication != nil {
		return e.New(e.CodeVerifyFailed, "selected original output differs from exact named inputs or is not output-local")
	}
	// bindObserved checks the one named slot, including native platform and disk
	// Plan identity. Its incomplete aggregate result is discarded, never issued.
	_, err := bindObserved(prepared, []Artifact{local.Outputs[0].Output})
	return err
}

func reserveBootProof(proof *sign.VerifiedOCIEvidence, seen map[string]bool, total *int64) error {
	_, signature, attestation, err := proof.Bundles()
	if err != nil {
		return err
	}
	for _, raw := range [][]byte{signature, attestation} {
		if len(raw) > 8<<20 {
			return e.New(e.CodeEngineCapability, "boot original proof exceeds individual bundle bound")
		}
		digest := pipeline.FileDigest(raw)
		if seen[digest] {
			continue
		}
		if int64(len(raw)) > (16<<20)-*total {
			return e.New(e.CodeEngineCapability, "boot original proofs exceed their complete reservation")
		}
		seen[digest] = true
		*total += int64(len(raw))
	}
	return nil
}

func bootProofInvocation(proof *sign.VerifiedOCIEvidence) (string, error) {
	raw, err := proof.ProvenancePredicate()
	if err != nil {
		return "", err
	}
	var predicate provenance.Predicate
	if err := json.Unmarshal(raw, &predicate); err != nil {
		return "", e.Wrap(e.CodeProvenanceMissing, err, "decoding original boot publication invocation")
	}
	invocation := predicate.RunDetails.Metadata["invocationId"]
	if err := factory.ValidatePublicationInvocation(invocation, invocation); err != nil {
		return "", err
	}
	return invocation, nil
}
