// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func (writer layoutWriter) importImageIndexLayout(ctx context.Context, input ObjectInput) (result Descriptor, digest string, err error) {
	if err := admitImageIndexInput(input); err != nil {
		return Descriptor{}, "", err
	}
	index, err := OpenImageIndex(ctx, input.Path, input.BlobDigest, input.Output.ImageIndex.RequiredPlatforms)
	if err != nil {
		return Descriptor{}, "", err
	}
	defer func() {
		if closeErr := index.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
			result, digest = Descriptor{}, ""
		}
	}()
	description, err := index.Description()
	if err != nil {
		return Descriptor{}, "", err
	}
	object := PublishedObject{Output: input.Output, Name: input.Name, Kind: input.Kind, ArtifactType: input.ArtifactType,
		DescriptorDigest: description.Digest, Size: description.Size, SemanticDigest: input.SemanticDigest,
		BlobDigest: input.BlobDigest, OS: input.OS, Arch: input.Arch}
	return writer.importHeldImageIndex(ctx, object, index)
}

// importHeldImageIndex borrows an already verified closure without another layout
// or snapshot. The operation gate joins concurrent Close across shallow handles;
// the caller retains ownership and closes the index after the import finishes.
func (writer layoutWriter) importHeldImageIndex(ctx context.Context, object PublishedObject, index *ImageIndex) (Descriptor, string, error) {
	if err := deliveryContext(ctx); err != nil {
		return Descriptor{}, "", err
	}
	if index == nil {
		return Descriptor{}, "", e.New(e.CodeUsage, "image index import requires a capability")
	}
	if index.imageIndexState == nil || index.operation == nil {
		return Descriptor{}, "", e.New(e.CodeVerifyFailed, "image index capability was not issued by delivery")
	}
	select {
	case <-ctx.Done():
		return Descriptor{}, "", e.Wrap(e.CodeTimeout, ctx.Err(), "image index import canceled while waiting for an active operation")
	case index.operation <- struct{}{}:
	}
	defer func() { <-index.operation }()
	if err := deliveryContext(ctx); err != nil {
		return Descriptor{}, "", err
	}
	index.mu.Lock()
	err := index.usable()
	closure, description := index.closure, index.description
	index.mu.Unlock()
	if err != nil {
		return Descriptor{}, "", err
	}
	if err := validatePublishedImageIndex(object, description); err != nil {
		return Descriptor{}, "", err
	}
	digests := make([]string, 0, len(closure.blobs))
	for digest := range closure.blobs {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		if err := deliveryContext(ctx); err != nil {
			return Descriptor{}, "", err
		}
		if _, err := writer.addBlobHeld(ctx, closure.blobs[digest], MediaOctetStream); err != nil {
			return Descriptor{}, "", err
		}
	}
	descriptor, err := writer.addBlobHeld(ctx, closure.index, MediaOCIIndex)
	if err != nil {
		return Descriptor{}, "", err
	}
	if err := deliveryContext(ctx); err != nil {
		return Descriptor{}, "", err
	}
	return descriptor, description.Digest, nil
}

// openPublishedImageIndex captures only this output's reachable bytes from a
// publication layout. The same runnable-image owner checks platforms and native
// image semantics; these paths and metadata do not restore release authority.
func openPublishedImageIndex(ctx context.Context, root string, object PublishedObject) (*ImageIndex, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if err := validateImageIndexObject(object); err != nil {
		return nil, err
	}
	closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
	complete := false
	defer func() {
		if !complete {
			closure.close()
		}
	}()
	children, err := populatePublishedImageIndex(ctx, object, closure, func(descriptor Descriptor, maximum int64) (*heldfile.File, error) {
		return holdBlob(ctx, root, descriptor, maximum)
	})
	if err != nil {
		return nil, err
	}
	complete = true
	return newImageIndex(closure, children), nil
}

// The caller owns or borrows every captured file through the complete operation.
// This shared verifier checks the complete aggregate before any child selection.
func populatePublishedImageIndex(ctx context.Context, object PublishedObject, closure *imageIndexClosure,
	open func(Descriptor, int64) (*heldfile.File, error),
) ([]ImageIndexChild, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if err := validateImageIndexObject(object); err != nil {
		return nil, err
	}
	var err error
	closure.index, err = open(Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return nil, err
	}
	raw, err := closure.index.ReadAllContext(ctx, 8<<20)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading publication image index"))
	}
	var index runnableIndex
	if err := decodeImageJSON(raw, &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex || index.ArtifactType != "" ||
		len(index.Manifests) != len(object.Output.ImageIndex.RequiredPlatforms) {
		return nil, e.New(e.CodeVerifyFailed, "publication image index does not match its required membership")
	}
	// Capture closure structure with bounded reads. Actual native-image validation
	// is centralized in imageIndexClosure.verify, including config/diffID checks.
	capture := func(descriptor Descriptor, maximum int64) (*heldfile.File, error) {
		if file := closure.blobs[descriptor.Digest]; file != nil {
			checked, _, err := closure.open(ctx, descriptor, maximum)
			return checked, err
		}
		if len(closure.blobs) >= maxDeliveryObjects*8 || descriptor.Size < 0 || descriptor.Size > maxDeliveryFileBytes-closure.bytes {
			return nil, e.New(e.CodeVerifyFailed, "publication image index closure exceeds its object or byte bound")
		}
		file, err := open(descriptor, maximum)
		if err != nil {
			return nil, err
		}
		closure.blobs[descriptor.Digest] = file
		closure.bytes += file.Size()
		return file, nil
	}
	for _, child := range index.Manifests {
		if child.MediaType != MediaOCIManifest || child.ArtifactType != "" || child.Platform == nil {
			return nil, e.New(e.CodeVerifyFailed, "publication image index requires native image children with explicit platforms")
		}
		file, err := capture(child.Descriptor, 8<<20)
		if err != nil {
			return nil, err
		}
		raw, err := file.ReadAllContext(ctx, 8<<20)
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading publication native image"))
		}
		var manifest ociManifest
		if err := decodeImageJSON(raw, &manifest); err != nil {
			return nil, err
		}
		if _, err := capture(manifest.Config, 8<<20); err != nil {
			return nil, err
		}
		for _, layer := range manifest.Layers {
			if _, err := capture(layer, maxDeliveryFileBytes); err != nil {
				return nil, err
			}
		}
	}
	platforms, err := admitImagePlatforms(ctx, object.Output.ImageIndex.RequiredPlatforms)
	if err != nil {
		return nil, err
	}
	children, err := closure.verify(ctx, platforms, true)
	if err != nil {
		return nil, err
	}
	if err := validatePublishedImageIndex(object, ImageIndexDescription{
		Digest: closure.index.Digest(), Size: closure.index.Size(), Children: children,
	}); err != nil {
		return nil, err
	}
	return children, nil
}
