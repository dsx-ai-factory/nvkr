// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// ExtractBinaryBundle materializes a named bundle from this owner's checked
// publication. It reads only captured bytes, so replacing the source layout
// cannot substitute an executable after root authentication.
func (publication *Publication) ExtractBinaryBundle(ctx context.Context, name, out string) (BinaryBundle, error) {
	if err := deliveryContext(ctx); err != nil {
		return BinaryBundle{}, err
	}
	if publication == nil || publication.publicationState == nil {
		return BinaryBundle{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || len(publication.files) == 0 {
		return BinaryBundle{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	return extractBinaryBundleWith(ctx, publication.inspection, name, out,
		func(descriptor Descriptor, max int64) ([]byte, error) {
			return publication.readHeldBlob(ctx, descriptor, max)
		}, func(descriptor Descriptor) (*heldfile.File, func(), error) {
			file, err := publication.heldBlob(descriptor, maxDeliveryFileBytes)
			// The publication lock holds the borrowed layer through extraction.
			return file, func() {}, err
		})
}

// ExtractObject materializes a named single-payload object from captured bytes.
// The existing extractor checks its name and payload against the checked root.
func (publication *Publication) ExtractObject(ctx context.Context, name, out string) (PublishedObject, error) {
	if err := deliveryContext(ctx); err != nil {
		return PublishedObject{}, err
	}
	if publication == nil || publication.publicationState == nil {
		return PublishedObject{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || len(publication.files) == 0 {
		return PublishedObject{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	object, payload, err := readObject(publication.inspection, name, func(descriptor Descriptor, max int64) ([]byte, error) {
		return publication.readHeldBlob(ctx, descriptor, max)
	})
	if err != nil {
		return PublishedObject{}, err
	}
	if err := writeExtractedObject(ctx, out, payload); err != nil {
		return PublishedObject{}, err
	}
	return object, nil
}

// The caller holds publication.mu for the complete use of returned capabilities.
func (publication *Publication) heldBlob(descriptor Descriptor, max int64) (*heldfile.File, error) {
	for _, file := range publication.files[1:] {
		if file.Digest() == descriptor.Digest {
			if descriptor.Size < 0 || descriptor.Size != file.Size() || descriptor.Size > max {
				return nil, e.New(e.CodeVerifyFailed, "publication blob differs from its bounded descriptor")
			}
			return file, nil
		}
	}
	return nil, e.New(e.CodeVerifyFailed, "publication has no captured blob for descriptor %q", descriptor.Digest)
}

func (publication *Publication) readHeldBlob(ctx context.Context, descriptor Descriptor, max int64) ([]byte, error) {
	file, err := publication.heldBlob(descriptor, max)
	if err != nil {
		return nil, err
	}
	raw, err := file.ReadAllContext(ctx, max)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading captured publication blob"))
	}
	if int64(len(raw)) != descriptor.Size || digestBytes(raw) != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "captured publication blob differs from its descriptor")
	}
	return raw, nil
}
