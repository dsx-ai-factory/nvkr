// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// wrapMkosiTar preserves mkosi's exact tar bytes as the sole OCI layer. It runs
// after the assembly process and all descendants have exited. The private output
// remains descriptive until the caller verifies its contents and reproduction.
func wrapMkosiTar(ctx context.Context, work string, operation mkosi.Invocation, platform, destination string) (returnErr error) {
	if ctx == nil || operation.Format != "tar" || !strings.HasPrefix(operation.Output, "/work/") ||
		(platform != "linux/amd64" && platform != "linux/arm64") {
		return e.New(e.CodeEngineCapability, "runtime OCI wrapping requires a native tar assembly")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime OCI wrapping canceled")
	}
	file, info, err := openRegularBeneath(work, strings.TrimPrefix(operation.Output, "/work/"))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening mkosi tar output")
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if info.Size() <= 0 || info.Size() > rootfs.MaxLayerBytes {
		return e.New(e.CodeVerifyFailed, "mkosi tar exceeds runtime layer bounds")
	}
	digest, size, _, err := hashOpenRegularContext(ctx, file, info)
	if err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "measuring mkosi tar"), "runtime OCI wrapping")
	}
	hash, err := v1.NewHash(digest)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding mkosi tar digest")
	}
	layer := &mkosiTarLayer{ctx: ctx, file: file, size: size, digest: hash}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "constructing runtime OCI layer")
	}
	image, err = mutate.ConfigFile(image, &v1.ConfigFile{
		Architecture: strings.TrimPrefix(platform, "linux/"), OS: "linux",
		Created: v1.Time{Time: time.Unix(0, 0).UTC()},
		RootFS:  v1.RootFS{Type: "layers", DiffIDs: []v1.Hash{hash}},
	})
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "constructing deterministic runtime OCI configuration")
	}
	image = mutate.ConfigMediaType(mutate.MediaType(image, types.OCIManifestSchema1), types.OCIConfigJSON)
	subject, err := image.Digest()
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "measuring runtime OCI manifest")
	}
	manifestSize, err := image.Size()
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "sizing runtime OCI manifest")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "runtime OCI wrapping canceled before output")
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "reserving fresh runtime OCI layout")
	}
	writer, err := layout.Write(destination, empty.Index)
	if err == nil {
		err = writer.WriteImage(image)
	}
	if err == nil {
		// Emit only the descriptor fields admitted by the runtime contract;
		// AppendImage also infers optional artifact metadata from the config.
		err = writer.AppendDescriptor(v1.Descriptor{MediaType: types.OCIManifestSchema1, Digest: subject, Size: manifestSize})
	}
	if err != nil {
		return mapCancellation(ctx, e.Wrap(e.CodeBuildFailed, err, "writing runtime OCI layout"), "runtime OCI wrapping")
	}
	// Rehash the complete closure after streaming, before any content inspection
	// or production receipt. A changed source cannot become an accepted layer.
	return verifyOCILayout(ctx, destination, subject.String())
}

type mkosiTarLayer struct {
	ctx    context.Context
	file   *os.File
	size   int64
	digest v1.Hash
}

func (layer *mkosiTarLayer) Digest() (v1.Hash, error) { return layer.digest, nil }
func (layer *mkosiTarLayer) DiffID() (v1.Hash, error) { return layer.digest, nil }
func (layer *mkosiTarLayer) Size() (int64, error)     { return layer.size, nil }
func (layer *mkosiTarLayer) MediaType() (types.MediaType, error) {
	return types.OCIUncompressedLayer, nil
}

// The OCI library calls the stored representation Compressed even when the
// media type is an uncompressed tar. Use a held descriptor, never reopen a path.
func (layer *mkosiTarLayer) Compressed() (io.ReadCloser, error) {
	if err := layer.ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(pipelineContextReader{ctx: layer.ctx, source: io.NewSectionReader(layer.file, 0, layer.size)}), nil
}

func (layer *mkosiTarLayer) Uncompressed() (io.ReadCloser, error) { return layer.Compressed() }
