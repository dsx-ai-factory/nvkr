// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// AttestPreparedMetadata replaces the raw SBOMs in a bounded preparation control
// layout with attested bundles. It neither reads nor transports payloads and
// issues no payload capability. The caller admits the exact expected root against
// its Plan and Run; the callback owns cryptographic creation and verification.
// Source is retained and must have no concurrent writers.
func AttestPreparedMetadata(ctx context.Context, source, out, expectedRoot string, attest func(context.Context, string, []byte) ([]byte, error)) (result Result, err error) {
	defer attestationBoundary(ctx, &err, "attesting preparation metadata")
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if attest == nil {
		return Result{}, e.New(e.CodeEngineCapability, "SBOM attestation adapter is required")
	}
	if !canonical.IsDigest(expectedRoot) {
		return Result{}, e.New(e.CodeUsage, "preparation metadata requires an exact expected root")
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
	inspection, err := InspectMetadata(ctx, source)
	if err != nil {
		return Result{}, err
	}
	if inspection.RootDigest != expectedRoot {
		return Result{}, e.New(e.CodeVerifyFailed, "preparation metadata changed after admission")
	}
	if err := requirePreparedEvidence(inspection.Root); err != nil {
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
	for i, object := range root.Objects {
		if object.Evidence == nil {
			if err := copyAttestationControls(ctx, writer, source, object); err != nil {
				return Result{}, err
			}
			continue
		}
		root.Objects[i], err = attestPreparedObject(ctx, writer, source, root, object, attest)
		if err != nil {
			return Result{}, err
		}
	}
	digest, semantic, err := writer.writePublicationMetadata(root, nil)
	if err != nil {
		return Result{}, err
	}
	final, err := InspectMetadata(ctx, stage)
	if err != nil {
		return Result{}, err
	}
	if final.RootDigest != digest {
		return Result{}, e.New(e.CodeVerifyFailed, "attested metadata changed before publication")
	}
	if err := comparePreparedInventory(ctx, source, inventory); err != nil {
		return Result{}, err
	}
	if err := publishAttestedLayout(ctx, stage, out); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: requestedOutput, RootDigest: digest, SemanticDigest: semantic}, nil
}

func requirePreparedEvidence(root PublicationRoot) error {
	if root.Release == nil {
		return e.New(e.CodeEngineCapability, "SBOM attestation requires release metadata")
	}
	count := 0
	for _, object := range root.Objects {
		if object.Evidence != nil {
			count++
			if object.ArtifactType != "application/vnd.cyclonedx+json" {
				return e.New(e.CodeVerifyFailed, "prepared evidence must be a CycloneDX document")
			}
		}
	}
	if count == 0 {
		return e.New(e.CodeProvenanceMissing, "preparation contains no SBOM documents")
	}
	return nil
}

// Copy only descriptor-authenticated control blobs. Output layers are never
// opened; evidence layers are bounded by the existing metadata reader.
func copyAttestationControls(ctx context.Context, writer layoutWriter, source string, object PublishedObject) error {
	var controls []Descriptor
	if object.Output != nil && object.Output.ImageIndex != nil {
		var err error
		controls, _, err = imageIndexMetadata(ctx, source, object)
		if err != nil {
			return err
		}
	} else {
		manifest, err := flatManifest(ctx, source, object)
		if err != nil {
			return err
		}
		controls = []Descriptor{{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size}, manifest.Config}
		if object.Evidence != nil {
			raw, err := metadataEvidence(ctx, source, object)
			if err != nil {
				return err
			}
			if _, err := writer.addBlobBytes(raw, manifest.Layers[0].MediaType); err != nil {
				return err
			}
		}
	}
	for _, descriptor := range controls {
		raw, err := readBlob(ctx, source, descriptor, 8<<20)
		if err != nil {
			return err
		}
		if _, err := writer.addBlobBytes(raw, descriptor.MediaType); err != nil {
			return err
		}
	}
	return nil
}

func attestationBoundary(ctx context.Context, err *error, operation string) {
	if *err != nil {
		var coded *e.Error
		if !errors.As(*err, &coded) {
			*err = e.Wrap(e.CodeInternal, *err, "%s", operation)
		}
		*err = classifyContext(ctx, *err)
	}
}
