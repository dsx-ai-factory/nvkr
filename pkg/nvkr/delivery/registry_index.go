// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// fetchRegistryImageIndex captures the exact remote closure into bounded held
// files. Transport follows only descriptors in the requested immutable index;
// the existing ImageIndex owner validates runnable-image semantics and platforms.
func fetchRegistryImageIndex(ctx context.Context, ref name.Digest, expectedSize int64,
	requiredPlatforms []string, options []remote.Option,
) (*ImageIndex, error) {
	platforms, err := admitImagePlatforms(ctx, requiredPlatforms)
	if err != nil {
		return nil, err
	}
	root := Descriptor{MediaType: MediaOCIIndex, Digest: ref.DigestStr(), Size: expectedSize}
	raw, err := registryManifest(ctx, ref, root, options)
	if err != nil {
		return nil, err
	}
	var manifest runnableIndex
	if err := decodeImageJSON(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIIndex || manifest.ArtifactType != "" || len(manifest.Manifests) != len(platforms) {
		return nil, e.New(e.CodeVerifyFailed, "registry image index has unexpected child membership")
	}
	closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
	complete := false
	defer func() {
		if !complete {
			closure.close()
		}
	}()
	closure.index, err = heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "registry image index", MaxBytes: 8 << 20, ExpectedDigest: root.Digest})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding registry image index"))
	}
	for _, child := range manifest.Manifests {
		if child.MediaType != MediaOCIManifest || child.ArtifactType != "" || child.Platform == nil {
			return nil, e.New(e.CodeVerifyFailed, "registry image index contains a non-image child")
		}
		if closure.blobs[child.Digest] != nil {
			return nil, e.New(e.CodeVerifyFailed, "registry image index repeats a child")
		}
		childRaw, err := registryManifest(ctx, ref.Context().Digest(child.Digest), child.Descriptor, options)
		if err != nil {
			return nil, err
		}
		if len(closure.blobs) >= maxDeliveryObjects*8 || int64(len(childRaw)) > maxDeliveryFileBytes-closure.bytes {
			return nil, e.New(e.CodeVerifyFailed, "registry image closure exceeds object or byte bounds")
		}
		held, err := heldfile.FromBytesContext(ctx, childRaw, heldfile.Options{Label: "registry native manifest", MaxBytes: 8 << 20, ExpectedDigest: child.Digest})
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding registry native manifest"))
		}
		closure.blobs[child.Digest] = held
		closure.bytes += held.Size()
		var image ociManifest
		if err := decodeImageJSON(childRaw, &image); err != nil {
			return nil, err
		}
		if len(image.Layers) > maxDeliveryObjects*8-1 {
			return nil, e.New(e.CodeVerifyFailed, "registry image exceeds blob count bound")
		}
		if err := captureRegistryBlob(ctx, closure, ref.Context(), image.Config, 8<<20, options); err != nil {
			return nil, err
		}
		for _, layer := range image.Layers {
			if err := captureRegistryBlob(ctx, closure, ref.Context(), layer, maxDeliveryFileBytes, options); err != nil {
				return nil, err
			}
		}
	}
	children, err := closure.verify(ctx, platforms, true)
	if err != nil {
		return nil, err
	}
	complete = true
	return newImageIndex(closure, children), nil
}

func registryManifest(ctx context.Context, ref name.Digest, expected Descriptor, options []remote.Option) ([]byte, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if !isDigest(expected.Digest) || ref.DigestStr() != expected.Digest || expected.Size <= 0 || expected.Size > 8<<20 {
		return nil, e.New(e.CodeVerifyFailed, "registry image manifest has an invalid descriptor")
	}
	remoteDescriptor, err := remote.Get(ref, options...)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching registry image manifest"))
	}
	raw := remoteDescriptor.Manifest
	if int64(len(raw)) != expected.Size || digestBytes(raw) != expected.Digest ||
		remoteDescriptor.Digest.String() != expected.Digest || string(remoteDescriptor.MediaType) != expected.MediaType {
		return nil, e.New(e.CodeVerifyFailed, "registry substituted image manifest bytes or media type")
	}
	return raw, nil
}

func captureRegistryBlob(ctx context.Context, closure *imageIndexClosure, repository name.Repository,
	descriptor Descriptor, maximum int64, options []remote.Option,
) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > maximum {
		return e.New(e.CodeVerifyFailed, "registry image blob has an invalid descriptor")
	}
	if existing := closure.blobs[descriptor.Digest]; existing != nil {
		if existing.Size() != descriptor.Size {
			return e.New(e.CodeVerifyFailed, "registry image repeats a blob with a different size")
		}
		return nil
	}
	if len(closure.blobs) >= maxDeliveryObjects*8 || descriptor.Size > maxDeliveryFileBytes-closure.bytes {
		return e.New(e.CodeVerifyFailed, "registry image closure exceeds object or byte bounds")
	}
	layer, err := remote.Layer(repository.Digest(descriptor.Digest), options...)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "opening registry image blob"))
	}
	stream, err := layer.Compressed()
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching registry image blob"))
	}
	held, captureErr := heldfile.FromReaderContext(ctx, stream, heldfile.Options{
		Label: "registry image blob", MaxBytes: max(descriptor.Size, 1), ExpectedDigest: descriptor.Digest,
	})
	closeErr := stream.Close()
	if captureErr != nil {
		code := e.CodeVerifyFailed
		var networkError net.Error
		switch {
		case errors.Is(captureErr, context.Canceled), errors.Is(captureErr, context.DeadlineExceeded):
			code = e.CodeTimeout
		case errors.As(captureErr, &networkError):
			code = e.CodeUnavailable
			if networkError.Timeout() {
				code = e.CodeTimeout
			}
		case errors.Is(captureErr, io.EOF), errors.Is(captureErr, io.ErrUnexpectedEOF):
			code = e.CodeUnavailable
		}
		return classifyContext(ctx, e.Wrap(code, captureErr, "capturing registry image blob"))
	}
	if closeErr != nil || held.Size() != descriptor.Size {
		_ = held.Close()
		if closeErr != nil {
			return classifyContext(ctx, e.Wrap(e.CodeUnavailable, closeErr, "closing registry image blob"))
		}
		return e.New(e.CodeVerifyFailed, "registry image blob differs from its declared size")
	}
	closure.blobs[descriptor.Digest] = held
	closure.bytes += held.Size()
	return nil
}

// registryIndexLayout lends a private standard-reader view of held exact bytes.
// The ImageIndex keeps owning validation; this adapter creates no new identity.
func registryIndexLayout(ctx context.Context, index *ImageIndex) (v1.ImageIndex, func(), error) {
	noop := func() {}
	if err := deliveryContext(ctx); err != nil {
		return nil, noop, err
	}
	if _, err := index.Description(); err != nil {
		return nil, noop, err
	}
	workspace, err := os.MkdirTemp("", "nvkr-index-registry-*")
	if err != nil {
		return nil, noop, e.Wrap(e.CodeInternal, err, "creating registry index workspace")
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	root := filepath.Join(workspace, "image")
	if err := index.WriteLayout(ctx, root); err != nil {
		cleanup()
		return nil, noop, err
	}
	image, err := layout.Path(root).ImageIndex()
	if err != nil {
		cleanup()
		return nil, noop, e.Wrap(e.CodeVerifyFailed, err, "opening held registry index layout")
	}
	return image, cleanup, nil
}

// materializeRegistryImageIndex imports only this checked output's exact closure
// into Pull's private publication stage; the publication's top-level index stays
// unchanged. The same flat import owner used by release construction rechecks it.
func materializeRegistryImageIndex(ctx context.Context, stage string, object PublishedObject, index *ImageIndex) error {
	description, err := index.Description()
	if err != nil {
		return err
	}
	if err := validatePublishedImageIndex(object, description); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp("", "nvkr-index-pull-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating index import workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	root := filepath.Join(workspace, "image")
	if err := index.WriteLayout(ctx, root); err != nil {
		return err
	}
	descriptor, _, err := (layoutWriter{root: stage}).importFlatLayout(ctx, ObjectInput{
		Name: object.Name, Kind: object.Kind, ArtifactType: object.ArtifactType, SemanticDigest: object.SemanticDigest,
		Path: root, BlobDigest: object.DescriptorDigest, Output: object.Output, Layout: true,
	})
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "importing registry image index"))
	}
	if descriptor.MediaType != MediaOCIIndex || descriptor.Digest != object.DescriptorDigest || descriptor.Size != object.Size {
		return e.New(e.CodeVerifyFailed, "registry image import changed its exact index descriptor")
	}
	return nil
}

func pushRegistryImageIndex(ctx context.Context, root string, object PublishedObject, ref name.Digest, options []remote.Option) error {
	if err := validateImageIndexObject(object); err != nil {
		return err
	}
	held, err := openPublishedImageIndex(ctx, root, object)
	if err != nil {
		return err
	}
	defer func() { _ = held.Close() }()
	index, cleanup, err := registryIndexLayout(ctx, held)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := remote.WriteIndex(ref, index, options...); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publishing image index %q", object.Name))
	}
	// HEAD or an index-only GET cannot establish that every required child and
	// blob is actually readable in this repository. Capture and verify all of it.
	readback, err := fetchRegistryImageIndex(ctx, ref, object.Size, object.Output.ImageIndex.RequiredPlatforms, options)
	if err != nil {
		return err
	}
	defer func() { _ = readback.Close() }()
	description, err := readback.Description()
	if err != nil {
		return err
	}
	return validatePublishedImageIndex(object, description)
}
