// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const maxDocumentArtifactBytes = 128 << 20

// PushDocumentArtifact carries bounded opaque document bytes in a deterministic
// single-layer OCI artifact. Config media type identifies the document family.
// It writes only an immutable manifest reference, with no signing authority.
// On upload failure the returned attempted reference may contain partial uploads.
func PushDocumentArtifact(ctx context.Context, repository, mediaType string, raw []byte, options RegistryOptions) (string, error) {
	if err := admitDocumentTransport(ctx, mediaType, int64(len(raw)), options); err != nil {
		return "", err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repo, err := name.NewRepository(repository, parse...)
	if err != nil || repo.Name() != repository || strings.ContainsAny(repository[strings.LastIndex(repository, "/")+1:], ":@") {
		return "", e.New(e.CodeUsage, "document publication requires a canonical untagged repository")
	}
	image, err := documentTransportImage(bytes.Clone(raw), mediaType)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "constructing document transport")
	}
	digest, err := image.Digest()
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "digesting document transport")
	}
	ref := repo.Digest(digest.String())
	if err := remote.Write(ref, image, registryOptions(ctx, options)...); err != nil {
		return ref.Name(), classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publishing document transport; destination may contain partial uploads"))
	}
	return ref.Name(), nil
}

// PullDocumentArtifact checks the exact standard wrapper and bounded payload.
// Returned bytes remain untrusted until the owning module authenticates them.
func PullDocumentArtifact(ctx context.Context, reference, mediaType string, limit int64, options RegistryOptions) ([]byte, error) {
	if err := admitDocumentTransport(ctx, mediaType, limit, options); err != nil {
		return nil, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil || ref.Name() != reference || !isDigest(ref.DigestStr()) {
		return nil, e.New(e.CodeUnpinnedSource, "document download requires a canonical digest reference")
	}
	descriptor, err := remote.Get(ref, registryOptions(ctx, options)...)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching document manifest"))
	}
	if len(descriptor.Manifest) > 8<<20 || digestBytes(descriptor.Manifest) != ref.DigestStr() || descriptor.MediaType != types.OCIManifestSchema1 {
		return nil, e.New(e.CodeVerifyFailed, "document manifest differs from its pinned identity")
	}
	image, err := descriptor.Image()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening document manifest")
	}
	manifest, err := image.Manifest()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding document manifest")
	}
	if manifest.Config.MediaType != types.MediaType(mediaType) || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != types.MediaType(mediaType) || len(manifest.Layers[0].URLs) != 0 || manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > limit {
		return nil, e.New(e.CodeVerifyFailed, "document transport changes its type, membership or payload bound")
	}
	layer, err := remote.Layer(ref.Context().Digest(manifest.Layers[0].Digest.String()), registryOptions(ctx, options)...)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching document payload"))
	}
	stream, err := layer.Compressed()
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "opening document payload"))
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, manifest.Layers[0].Size+1))
	if err := errors.Join(readErr, stream.Close()); err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "reading document payload"))
	}
	if int64(len(raw)) != manifest.Layers[0].Size || digestBytes(raw) != manifest.Layers[0].Digest.String() {
		return nil, e.New(e.CodeVerifyFailed, "document payload differs from descriptor")
	}
	expected, err := documentTransportImage(raw, mediaType)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "reconstructing document transport")
	}
	digest, err := expected.Digest()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "digesting reconstructed document transport")
	}
	if digest.String() != ref.DigestStr() {
		return nil, e.New(e.CodeVerifyFailed, "document wrapper differs from standard transport")
	}
	return raw, nil
}

func documentTransportImage(raw []byte, mediaType string) (v1.Image, error) {
	image := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.MediaType(mediaType))
	return mutate.AppendLayers(image, static.NewLayer(raw, types.MediaType(mediaType)))
}

func admitDocumentTransport(ctx context.Context, mediaType string, limit int64, options RegistryOptions) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := options.Validate(); err != nil {
		return err
	}
	parsed, params, err := mime.ParseMediaType(mediaType)
	if err != nil || parsed != mediaType || len(params) != 0 || len(mediaType) > 200 {
		return e.New(e.CodeUsage, "document transport requires a canonical media type")
	}
	if limit <= 0 || limit > maxDocumentArtifactBytes {
		return e.New(e.CodeVerifyFailed, "document transport requires a positive bound at most 128 MiB")
	}
	return nil
}
