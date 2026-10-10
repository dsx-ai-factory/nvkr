// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// imageIndexMetadata checks the exact index, native manifests and configs. Layer
// descriptors are inventoried without reading payloads or issuing authority.
func imageIndexMetadata(ctx context.Context, root string, object PublishedObject) ([]Descriptor, []Descriptor, error) {
	if err := validateImageIndexObject(object); err != nil {
		return nil, nil, err
	}
	descriptor := Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}
	raw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return nil, nil, err
	}
	var index runnableIndex
	if err := decodeImageJSON(raw, &index); err != nil {
		return nil, nil, err
	}
	if index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex || index.ArtifactType != "" || len(index.Manifests) != len(object.Output.ImageIndex.Children) {
		return nil, nil, e.New(e.CodeVerifyFailed, "metadata image index differs from required membership")
	}
	controls := []Descriptor{descriptor}
	var payloads []Descriptor
	description := ImageIndexDescription{Digest: descriptor.Digest, Size: descriptor.Size}
	for _, child := range index.Manifests {
		if child.MediaType != MediaOCIManifest || child.ArtifactType != "" || child.Platform == nil {
			return nil, nil, e.New(e.CodeVerifyFailed, "metadata index requires native image descriptors with platforms")
		}
		manifest, err := metadataManifest(ctx, root, PublishedObject{DescriptorDigest: child.Digest, Size: child.Size, ArtifactType: child.MediaType})
		if err != nil {
			return nil, nil, err
		}
		if manifest.Subject != nil || manifest.Config.ArtifactType != "" {
			return nil, nil, e.New(e.CodeVerifyFailed, "metadata native image cannot be a subject-bearing artifact")
		}
		for _, layer := range manifest.Layers {
			if layer.ArtifactType != "" {
				return nil, nil, e.New(e.CodeVerifyFailed, "metadata image layer declares an artifact type")
			}
		}
		config, err := inspectImageMetadata(ctx, root, manifest)
		if err != nil {
			return nil, nil, err
		}
		platform := child.Platform
		if platform.OS != config.OS || platform.Architecture != config.Architecture || (platform.Variant != "" && (platform.Architecture != "arm64" || platform.Variant != "v8")) {
			return nil, nil, e.New(e.CodeVerifyFailed, "metadata index platform differs from native config")
		}
		controls = append(controls, child.Descriptor, manifest.Config)
		payloads = append(payloads, manifest.Layers...)
		description.Children = append(description.Children, ImageIndexChild{Platform: config.OS + "/" + config.Architecture, ManifestDigest: child.Digest, Size: child.Size})
	}
	sort.Slice(description.Children, func(i, j int) bool { return description.Children[i].Platform < description.Children[j].Platform })
	if err := validatePublishedImageIndex(object, description); err != nil {
		return nil, nil, err
	}
	return controls, payloads, nil
}
