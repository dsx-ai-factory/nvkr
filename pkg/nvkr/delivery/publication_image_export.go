// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// ExportImage writes one exact native image from this captured publication.
// Aggregate outputs require an explicit platform and retain complete sibling
// validation before selection. The archive preserves image bytes and establishes
// transport integrity only; it grants no signer or qualification authority.
func (publication *Publication) ExportImage(ctx context.Context, objectName, reference, platform, out string) (PublishedImageExport, error) {
	ref, err := admitImageExport(ctx, objectName, reference, platform, out)
	if err != nil {
		return PublishedImageExport{}, err
	}
	if publication == nil {
		return PublishedImageExport{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || len(publication.files) == 0 {
		return PublishedImageExport{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	object, err := publishedImageOutput(publication.inspection, objectName)
	if err != nil {
		return PublishedImageExport{}, err
	}
	descriptor := Descriptor{Digest: object.DescriptorDigest, Size: object.Size, MediaType: object.ArtifactType}
	native := object
	if object.Output.ImageIndex != nil {
		if platform == "" {
			return PublishedImageExport{}, e.New(e.CodeUsage, "aggregate image export requires an explicit platform")
		}
		// Borrow only this image's complete reachable closure. The publication
		// lock owns all files, including siblings, through verification and export.
		closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
		children, err := populatePublishedImageIndex(ctx, object, closure, publication.heldBlob)
		if err != nil {
			return PublishedImageExport{}, err
		}
		descriptor = Descriptor{}
		for _, child := range children {
			if child.Platform == platform {
				descriptor = Descriptor{Digest: child.ManifestDigest, Size: child.Size, MediaType: MediaOCIManifest}
			}
		}
		if descriptor.Digest == "" {
			return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image output does not contain selected platform")
		}
		// Reuse the native-image verifier on the selected child of the checked
		// aggregate. These private fields are joined to the held child above.
		native.DescriptorDigest, native.BlobDigest, native.Size = descriptor.Digest, descriptor.Digest, descriptor.Size
		native.ArtifactType = descriptor.MediaType
		native.OS, native.Arch, _ = strings.Cut(platform, "/")
	}
	if ref.DigestStr() != descriptor.Digest {
		return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image export reference substitutes the selected native digest")
	}
	blobs := make(map[string]*heldfile.File)
	var total int64
	open := func(ctx context.Context, descriptor Descriptor, maximum int64) (*heldfile.File, func(), error) {
		if err := deliveryContext(ctx); err != nil {
			return nil, nil, err
		}
		file, err := publication.heldBlob(descriptor, maximum)
		if err != nil {
			return nil, nil, err
		}
		if blobs[descriptor.Digest] == nil {
			if len(blobs) >= maxDeliveryObjects*8 || file.Size() > maxDeliveryFileBytes-total {
				return nil, nil, e.New(e.CodeVerifyFailed, "selected image closure exceeds its object or byte bound")
			}
			blobs[descriptor.Digest] = file
			total += file.Size()
		}
		return file, func() {}, nil
	}
	image, err := inspectPublishedContainerImageFrom(ctx, Inspection{Root: PublicationRoot{Objects: []PublishedObject{native}}}, objectName, open)
	if err != nil {
		return PublishedImageExport{}, err
	}
	actual := image.OS + "/" + image.Arch
	if platform != "" && platform != actual {
		return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image export platform differs from named output")
	}
	return exportHeldPublishedImage(ctx, ref, actual, out, descriptor, blobs)
}
