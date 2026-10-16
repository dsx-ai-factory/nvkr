// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// AttestPreparedEvidence consumes a private preparation layout into a new release
// layout, replacing raw SBOMs with bundles without copying unchanged payloads.
// The caller transfers exclusive ownership: source must have no other consumers
// or writers. Callback failures leave source in place. After the private claim,
// failures discard the consumed layout; callers must restore or rebuild it.
//
// The callback owns cryptographic creation and verification; this module enforces
// exact document, subject, membership and byte closure. Paths and the returned
// Result are descriptions, not a retained immutable verification capability.
func AttestPreparedEvidence(ctx context.Context, source, out string, attest func(context.Context, string, []byte) ([]byte, error)) (result Result, err error) {
	defer func() {
		if err != nil {
			var coded *e.Error
			if !errors.As(err, &coded) {
				err = e.Wrap(e.CodeInternal, err, "attesting prepared evidence")
			}
			err = classifyContext(ctx, err)
		}
	}()
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if attest == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM attestation adapter is required")
	}
	requestedOutput := out
	source, out, err = preparedAttestationPaths(ctx, source, out)
	if err != nil {
		return Result{}, err
	}
	inventory, err := preparedLayoutInventory(ctx, source)
	if err != nil {
		return Result{}, err
	}
	inspection, err := Inspect(ctx, source)
	if err != nil {
		return Result{}, err
	}
	if inspection.Root.Release == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM attestation requires release metadata")
	}
	sourceIndex, _, err := readIndex(ctx, source)
	if err != nil {
		return Result{}, err
	}
	stage, _, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	root := inspection.Root
	root.Objects = append([]PublishedObject(nil), root.Objects...)
	var direct []Descriptor
	unchanged := map[string]bool{}
	for i, object := range root.Objects {
		descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
		if object.Evidence == nil {
			if err := verifyDescriptor(ctx, source, descriptor, unchanged); err != nil {
				return Result{}, err
			}
			if object.Output == nil {
				for _, original := range sourceIndex.Manifests {
					if original.Digest == descriptor.Digest {
						direct = append(direct, original)
					}
				}
			}
			continue
		}
		root.Objects[i], err = attestPreparedObject(ctx, writer, source, root, object, attest)
		if err != nil {
			return Result{}, err
		}
	}
	digest, semantic, err := writer.writePublicationMetadata(root, direct)
	if err != nil {
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
		return Result{}, e.New(e.CodeVerifyFailed, "attested layout changed before publication")
	}
	if err := publishAttestedLayout(ctx, stage, out); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: requestedOutput, RootDigest: digest, SemanticDigest: semantic}, nil
}

func attestPreparedObject(ctx context.Context, writer layoutWriter, source string, root PublicationRoot, object PublishedObject, attest func(context.Context, string, []byte) ([]byte, error)) (PublishedObject, error) {
	if object.ArtifactType != "application/vnd.cyclonedx+json" {
		return PublishedObject{}, e.New(e.CodeVerifyFailed, "prepared evidence must be a CycloneDX document")
	}
	document, err := preparedEvidenceDocument(ctx, source, object)
	if err != nil {
		return PublishedObject{}, err
	}
	// Keep the authenticated predicate independent of callback-owned memory.
	bundle, err := attest(ctx, object.Evidence.SubjectDigest, append([]byte(nil), document...))
	if err != nil {
		return PublishedObject{}, err
	}
	decoded, err := sbom.AttestedDocument(bundle, object.Evidence.SubjectDigest)
	if err != nil {
		return PublishedObject{}, err
	}
	if !bytes.Equal(decoded, document) {
		return PublishedObject{}, e.New(e.CodeVerifyFailed, "attestation changed the SBOM document")
	}
	owner, err := evidenceOwner(root.Objects, object.Evidence)
	if err != nil {
		return PublishedObject{}, err
	}
	descriptor, blob, err := writer.addBytesObject(ctx, ObjectInput{Name: object.Name, Kind: object.Kind, ArtifactType: sbom.BundleMediaType, LayerMediaType: sbom.BundleMediaType,
		SemanticDigest: object.SemanticDigest, Bytes: bundle, annotations: map[string]string{"dev.sigstore.bundle.predicateType": sbom.CycloneDXPredicateType}, subject: &Descriptor{MediaType: publishedManifestMediaType(owner), Digest: owner.DescriptorDigest, Size: owner.Size}})
	if err != nil {
		return PublishedObject{}, err
	}
	object.DescriptorDigest, object.BlobDigest = descriptor.Digest, blob
	object.Size, object.ArtifactType = descriptor.Size, sbom.BundleMediaType
	return object, nil
}

func preparedEvidenceDocument(ctx context.Context, source string, object PublishedObject) ([]byte, error) {
	manifest, err := flatManifest(ctx, source, object)
	if err != nil {
		return nil, err
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].Digest != object.BlobDigest || manifest.ArtifactType != object.ArtifactType {
		return nil, e.New(e.CodeVerifyFailed, "prepared SBOM differs from its exact evidence binding")
	}
	return readBlob(ctx, source, manifest.Layers[0], sbom.MaxDocumentBytes)
}

// EvidenceDocument returns the canonical CycloneDX document from either a
// private preparation layout or the final attestation-only release layout.
func EvidenceDocument(ctx context.Context, root, name string) (PublishedObject, []byte, error) {
	object, raw, err := ReadObject(ctx, root, name)
	if err != nil {
		return PublishedObject{}, nil, err
	}
	if object.Evidence == nil {
		return PublishedObject{}, nil, e.New(e.CodeVerifyFailed, "object is not SBOM evidence")
	}
	if object.ArtifactType == sbom.BundleMediaType {
		raw, err = sbom.AttestedDocument(raw, object.Evidence.SubjectDigest)
	}
	return object, raw, err
}

// RequireAttestedEvidence rejects raw preparation documents at production boundaries.
func RequireAttestedEvidence(inspection Inspection) error {
	if inspection.Root.Release == nil {
		return e.New(e.CodeVerifyFailed, "attested release metadata is required")
	}
	count := 0
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		count++
		if object.ArtifactType != sbom.BundleMediaType {
			return e.New(e.CodeProvenanceMissing, "release contains an unattested SBOM")
		}
	}
	if count == 0 {
		return e.New(e.CodeProvenanceMissing, "release contains no SBOM attestations")
	}
	return nil
}
