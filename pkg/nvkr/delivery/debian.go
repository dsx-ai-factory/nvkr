// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const outputFormatDebian = "debian-package"

// BuildDebianArtifact carries the exact original package as an ordinary one-layer
// OCI object. It does not rebuild, install, infer dependencies or change the raw
// producer subject. Factory authenticates that original producer/result pair.
func BuildDebianArtifact(ctx context.Context, out, name, source, expectedDigest string) (result Result, returnErr error) {
	if err := deliveryContext(ctx); err != nil {
		return result, err
	}
	if !outputNamePattern.MatchString(name) || !isDigest(expectedDigest) {
		return result, e.New(e.CodeVerifyFailed, "Debian carrier requires its exact named producer subject")
	}
	held, err := heldfile.OpenContext(ctx, source, heldfile.Options{Label: "original Debian package", MaxBytes: maxDeliveryFileBytes, ExpectedDigest: expectedDigest})
	if err != nil {
		return result, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing original Debian package"))
	}
	defer func() {
		if err := held.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeVerifyFailed, err, "closing original Debian package"))
			result = Result{}
		}
	}()
	if held.Size() <= 0 {
		return result, e.New(e.CodeVerifyFailed, "Debian carrier requires nonempty original bytes")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return result, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	descriptor, _, err := writer.addHeldObject(ctx, ObjectInput{Name: name, Kind: outputFormatDebian, ArtifactType: MediaDeliveryObject, LayerMediaType: MediaOctetStream, SemanticDigest: held.Digest(), BlobDigest: held.Digest()}, held)
	if err != nil {
		return result, err
	}
	index, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}})
	if err != nil {
		return result, err
	}
	if _, err := PayloadDescriptor(ctx, stage); err != nil {
		return result, err
	}
	if err := deliveryContext(ctx); err != nil {
		return result, err
	}
	if err := commit(); err != nil {
		return result, err
	}
	return Result{LayoutPath: out, RootDigest: index, SemanticDigest: held.Digest()}, nil
}

// InspectDebianArtifact checks the standard one-layer carrier and original raw
// binding without materializing its payload in a byte slice.
func InspectDebianArtifact(ctx context.Context, root, expectedDigest string) (Descriptor, error) {
	if !isDigest(expectedDigest) {
		return Descriptor{}, e.New(e.CodeVerifyFailed, "Debian carrier requires original raw digest")
	}
	descriptor, err := PayloadDescriptor(ctx, root)
	if err != nil {
		return Descriptor{}, err
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return Descriptor{}, err
	}
	if descriptor.ArtifactType != MediaDeliveryObject {
		return Descriptor{}, e.New(e.CodeVerifyFailed, "Debian carrier changes original raw package binding")
	}
	if err := validateDebianCarrier(ctx, root, manifest, expectedDigest); err != nil {
		return Descriptor{}, err
	}
	return descriptor, nil
}

// Both local inspection and selected-output pulls accept only the carrier
// emitted by BuildDebianArtifact. Raw package identity remains independent of
// the manifest's serialization and descriptor identity.
func validateDebianCarrier(ctx context.Context, root string, manifest ociManifest, expectedDigest string) error {
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || manifest.Subject != nil || len(manifest.Annotations) != 0 || manifest.ArtifactType != MediaDeliveryObject || manifest.Config.MediaType != MediaEmptyConfig || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaOctetStream || manifest.Layers[0].Digest != expectedDigest || manifest.Layers[0].Size <= 0 {
		return e.New(e.CodeVerifyFailed, "Debian carrier changes standard metadata or original raw package binding")
	}
	config, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return err
	}
	if string(config) != "{}" {
		return e.New(e.CodeVerifyFailed, "Debian carrier has nonstandard metadata config")
	}
	return nil
}
