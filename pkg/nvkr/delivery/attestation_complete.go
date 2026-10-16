// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// CompleteAttestedPreparation combines an exclusively owned full preparation
// with independently attested metadata. Both roots must match the caller's
// admitted Plan and Run. Every bundle passes the cryptographic verifier and must
// contain the identical original document; every other binding is immutable.
//
// After those checks, source is consumed by the same exclusive rename contract
// as AttestPreparedEvidence. Failure before the claim preserves source; failure
// after it requires restoring or rebuilding the disposable preparation. Attested
// metadata is retained. All inputs must have no concurrent writers.
func CompleteAttestedPreparation(ctx context.Context, source, attested, out, expectedPreparedRoot, expectedAttestedRoot string, verify func(context.Context, string, []byte) error) (result Result, err error) {
	defer attestationBoundary(ctx, &err, "completing attested preparation")
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if verify == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM verification adapter is required")
	}
	if !canonical.IsDigest(expectedPreparedRoot) || !canonical.IsDigest(expectedAttestedRoot) {
		return Result{}, e.New(e.CodeUsage, "attested completion requires exact expected roots")
	}
	requestedOutput := out
	source, out, err = preparedAttestationPaths(ctx, source, out)
	if err != nil {
		return Result{}, err
	}
	attested, _, err = preparedAttestationPaths(ctx, attested, out)
	if err != nil {
		return Result{}, err
	}
	if source == attested || strings.HasPrefix(source, attested+string(filepath.Separator)) || strings.HasPrefix(attested, source+string(filepath.Separator)) {
		return Result{}, e.New(e.CodeUsage, "prepared and attested inputs must not overlap")
	}
	inventory, err := preparedLayoutInventory(ctx, source)
	if err != nil {
		return Result{}, err
	}
	attestedInventory, err := preparedLayoutInventory(ctx, attested)
	if err != nil {
		return Result{}, err
	}
	prepared, err := Inspect(ctx, source)
	if err != nil {
		return Result{}, err
	}
	signed, err := InspectMetadata(ctx, attested)
	if err != nil {
		return Result{}, err
	}
	if prepared.RootDigest != expectedPreparedRoot || signed.RootDigest != expectedAttestedRoot {
		return Result{}, e.New(e.CodeVerifyFailed, "attestation inputs changed after admission")
	}
	if err := unchangedAttestationBindings(prepared.Root, signed.Root); err != nil {
		return Result{}, err
	}
	stage, _, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	unchanged, direct, err := prepareAttestedCompletion(ctx, writer, source, attested, prepared.Root, signed.Root, verify)
	if err != nil {
		return Result{}, err
	}
	digest, semantic, err := writer.writePublicationMetadata(signed.Root, direct)
	if err != nil {
		return Result{}, err
	}
	if digest != expectedAttestedRoot {
		return Result{}, e.New(e.CodeVerifyFailed, "completed metadata differs from the attested root")
	}
	if err := comparePreparedInventory(ctx, attested, attestedInventory); err != nil {
		return Result{}, err
	}
	if err := consumePreparedBlobs(ctx, source, stage, inventory, unchanged); err != nil {
		return Result{}, err
	}
	final, err := Inspect(ctx, stage)
	if err != nil {
		return Result{}, err
	}
	if final.RootDigest != digest {
		return Result{}, e.New(e.CodeVerifyFailed, "completed attestation changed before publication")
	}
	if err := publishAttestedLayout(ctx, stage, out); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: requestedOutput, RootDigest: digest, SemanticDigest: semantic}, nil
}

// Canonical equality covers the entire release, every output and every evidence
// binding. Only the four fields describing a raw document's bundle wrapper may
// change; adding future fields automatically keeps them immutable here.
func unchangedAttestationBindings(prepared, attested PublicationRoot) error {
	if err := requirePreparedEvidence(prepared); err != nil {
		return err
	}
	if err := RequireAttestedEvidence(Inspection{Root: attested}); err != nil {
		return err
	}
	if len(prepared.Objects) != len(attested.Objects) {
		return e.New(e.CodeVerifyFailed, "attestation changed publication membership")
	}
	normalized := attested
	normalized.Objects = append([]PublishedObject(nil), attested.Objects...)
	for i, original := range prepared.Objects {
		if original.Evidence != nil {
			normalized.Objects[i].DescriptorDigest = original.DescriptorDigest
			normalized.Objects[i].BlobDigest = original.BlobDigest
			normalized.Objects[i].Size = original.Size
			normalized.Objects[i].ArtifactType = original.ArtifactType
		}
	}
	original, err := canonical.Marshal(prepared)
	if err != nil {
		return err
	}
	compared, err := canonical.Marshal(normalized)
	if err != nil {
		return err
	}
	if !bytes.Equal(original, compared) {
		return e.New(e.CodeVerifyFailed, "attestation changed release, output or evidence bindings")
	}
	return nil
}

func prepareAttestedCompletion(ctx context.Context, writer layoutWriter, source, attested string, prepared, signed PublicationRoot, verify func(context.Context, string, []byte) error) (map[string]bool, []Descriptor, error) {
	index, _, err := readIndex(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	unchanged := map[string]bool{}
	var direct []Descriptor
	for i, object := range prepared.Objects {
		if object.Evidence != nil {
			if err := verifyCompletionEvidence(ctx, source, attested, object, signed.Objects[i], verify); err != nil {
				return nil, nil, err
			}
			if err := copyAttestationControls(ctx, writer, attested, signed.Objects[i]); err != nil {
				return nil, nil, err
			}
			continue
		}
		descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
		if err := verifyDescriptor(ctx, source, descriptor, unchanged); err != nil {
			return nil, nil, err
		}
		if object.Output == nil {
			for _, original := range index.Manifests {
				if original.Digest == descriptor.Digest {
					direct = append(direct, original)
				}
			}
		}
	}
	return unchanged, direct, nil
}

func verifyCompletionEvidence(ctx context.Context, source, attested string, original, signed PublishedObject, verify func(context.Context, string, []byte) error) error {
	document, err := preparedEvidenceDocument(ctx, source, original)
	if err != nil {
		return err
	}
	bundle, err := metadataEvidence(ctx, attested, signed)
	if err != nil {
		return err
	}
	decoded, err := sbom.AttestedDocument(bundle, original.Evidence.SubjectDigest)
	if err != nil {
		return err
	}
	if !bytes.Equal(document, decoded) {
		return e.New(e.CodeVerifyFailed, "attested bundle changed the original SBOM document")
	}
	return verify(ctx, original.Evidence.SubjectDigest, bundle)
}
