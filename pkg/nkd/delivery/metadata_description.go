// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// InspectMetadataDescription checks all metadata and evidence in a metadata-only
// or complete local layout. Output layers are never opened. Returned platforms
// describe the exact configs, not an observation of payloads or a trust receipt.
func InspectMetadataDescription(ctx context.Context, root, reference string) (PublicationDescription, error) {
	if err := ValidatePublicationReference(reference); err != nil {
		return PublicationDescription{}, err
	}
	inspection, err := inspectMetadata(ctx, root, true)
	if err != nil {
		return PublicationDescription{}, err
	}
	outputs, err := MetadataOutputs(inspection.Root)
	if err != nil {
		return PublicationDescription{}, err
	}
	platforms := map[string]publicationPlatform{}
	for _, object := range inspection.Root.Objects {
		platform, err := metadataObjectPlatform(ctx, root, object)
		if err != nil {
			return PublicationDescription{}, err
		}
		platforms[object.Name] = platform
	}
	return publicationDescription(reference, inspection, outputs, platforms)
}

func verifyMetadataInventory(ctx context.Context, root string, required, optional map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading metadata blob inventory")
	}
	for _, entry := range entries {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		digest := "sha256:" + entry.Name()
		if !entry.Type().IsRegular() || (!required[digest] && !optional[digest]) {
			return e.New(e.CodeVerifyFailed, "metadata layout contains unreachable or non-regular blob %q", entry.Name())
		}
	}
	return nil // Every required blob was already opened and hash/size checked.
}

func metadataObjectPlatform(ctx context.Context, root string, object PublishedObject) (publicationPlatform, error) {
	var platform publicationPlatform
	manifest, err := metadataManifest(ctx, root, object)
	if err != nil {
		return platform, err
	}
	for _, layer := range manifest.Layers {
		if !isDigest(layer.Digest) || layer.Size < 0 || layer.Size > maxDeliveryFileBytes || !validMediaType(layer.MediaType) {
			return platform, e.New(e.CodeVerifyFailed, "metadata layer descriptor is malformed")
		}
	}
	raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return platform, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return platform, err
	}
	if object.Output != nil && (object.Output.Format == "oci" || object.Output.Format == "disk-image") {
		if object.Output.TransportPath != "" {
			return platform, e.New(e.CodeVerifyFailed, "image metadata has an unexpected transport path")
		}
		if object.Output.Format == "disk-image" {
			platform, err = inspectDiskOutputMetadata(ctx, root, object, manifest)
			if err != nil {
				return platform, err
			}
		} else {
			config, err := inspectImageMetadata(ctx, root, manifest)
			if err != nil {
				return platform, err
			}
			platform = publicationPlatform{OS: config.OS, Arch: config.Architecture}
			if object.ArtifactType != manifest.MediaType || object.BlobDigest != object.DescriptorDigest {
				return platform, e.New(e.CodeVerifyFailed, "image metadata differs from its release binding")
			}
		}
		if platform.OS != object.OS || platform.Arch != object.Arch {
			return platform, e.New(e.CodeVerifyFailed, "output platform differs from its exact metadata config")
		}
		return platform, nil
	}
	// These profiles validate manifests/configs without reading artifact layers.
	// Never pass image or disk profiles here: their full verifier opens payloads.
	if types.MediaType(manifest.Config.MediaType).IsConfig() {
		return platform, e.New(e.CodeVerifyFailed, "unexpected image config in release metadata")
	}
	if err := verifyPublishedObjectBinding(ctx, root, object, Descriptor{MediaType: manifest.MediaType, Digest: object.DescriptorDigest, Size: object.Size}, nil); err != nil {
		return platform, err
	}
	if object.Output != nil && object.Output.Format == "executable" {
		var bundle BinaryBundle
		if manifest.Config.MediaType != MediaBinaryBundleConfig {
			return platform, e.New(e.CodeVerifyFailed, "executable metadata requires a binary bundle config")
		}
		if err := canonical.UnmarshalExact(raw, &bundle); err != nil {
			return platform, e.Wrap(e.CodeVerifyFailed, err, "decoding metadata binary config")
		}
		platform = publicationPlatform{OS: bundle.OS, Arch: bundle.Arch}
		if object.OS != "" && (object.OS != platform.OS || object.Arch != platform.Arch) {
			return platform, e.New(e.CodeVerifyFailed, "executable platform differs from its exact metadata config")
		}
	}
	if object.Output != nil {
		switch object.Output.Format {
		case outputFormatHelm:
			if object.Output.TransportPath != "" {
				return platform, e.New(e.CodeVerifyFailed, "Helm output has an unexpected transport path")
			}
		case "apt-repository":
			if object.Output.TransportPath != filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", object.Name)) {
				return platform, e.New(e.CodeVerifyFailed, "APT output has an unsafe transport binding")
			}
		}
	}
	return platform, nil
}
