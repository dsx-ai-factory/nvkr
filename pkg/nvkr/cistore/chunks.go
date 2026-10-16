// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	// Raw chunks stay below GHCR's 10 GB layer limit even for incompressible
	// archives. No individual chunk is a TAR filesystem or executable image.
	maxChunkBytes  int64 = 1 << 30
	maxChunkCount        = 64
	chunkMediaType       = types.MediaType("application/vnd.nvkr.ci-carrier.chunk.v1+octet-stream")
)

// chunkArchive hashes a held archive once, producing independently reopenable
// sections without compression, whole-archive buffering, or additional files.
// Production uses maxChunkBytes; smaller sections have the same bounded wire
// contract and permit focused multi-chunk contract tests.
func chunkArchive(ctx context.Context, archive io.ReaderAt, size, chunkSize int64) ([]v1.Layer, string, error) {
	if size <= 0 || size > transportarchive.MaxArchiveBytes || chunkSize <= 0 || chunkSize > maxChunkBytes || (size-1)/chunkSize+1 > maxChunkCount {
		return nil, "", e.New(e.CodeVerifyFailed, "CI carrier archive exceeds its chunk bounds")
	}
	whole := sha256.New()
	layers := make([]v1.Layer, 0, (size-1)/chunkSize+1)
	buffer := make([]byte, 128<<10)
	for offset := int64(0); offset < size; {
		length := min(chunkSize, size-offset)
		hash := sha256.New()
		n, err := io.CopyBuffer(io.MultiWriter(whole, hash), contextReader{ctx, io.NewSectionReader(archive, offset, length)}, buffer)
		if err != nil || n != length {
			return nil, "", safeIOError(ctx, e.CodeVerifyFailed, "hashing held CI archive chunk")
		}
		layers = append(layers, &rawChunkLayer{ctx: ctx, archive: archive, offset: offset, size: length, digest: v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(hash.Sum(nil))}})
		offset += length
	}
	return layers, "sha256:" + hex.EncodeToString(whole.Sum(nil)), nil
}

type rawChunkLayer struct {
	ctx     context.Context
	archive io.ReaderAt
	offset  int64
	size    int64
	digest  v1.Hash
}

func (l *rawChunkLayer) Digest() (v1.Hash, error)            { return l.digest, nil }
func (l *rawChunkLayer) DiffID() (v1.Hash, error)            { return l.digest, nil }
func (l *rawChunkLayer) Size() (int64, error)                { return l.size, nil }
func (l *rawChunkLayer) MediaType() (types.MediaType, error) { return chunkMediaType, nil }

// The Layer interface calls stored bytes "Compressed". These custom-media
// chunks deliberately have identical stored and uncompressed representations.
func (l *rawChunkLayer) Compressed() (io.ReadCloser, error) {
	if err := l.ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(contextReader{l.ctx, io.NewSectionReader(l.archive, l.offset, l.size)}), nil
}

func (l *rawChunkLayer) Uncompressed() (io.ReadCloser, error) { return l.Compressed() }

// Validate all descriptors before opening any payload. Arbitrary-sized chunks
// within these bounds are allowed; the immutable manifest binds their order and
// the locator additionally binds the complete reassembled archive.
func validateChunkManifest(manifest *v1.Manifest, archiveSize int64) error {
	if manifest == nil || manifest.SchemaVersion != 2 || manifest.MediaType != types.OCIManifestSchema1 || manifest.Config.MediaType != types.OCIConfigJSON || manifest.Config.Size <= 0 || manifest.Config.Size > MaxLocatorBytes*2 || !validSHA256(manifest.Config.Digest.String()) || len(manifest.Config.URLs) != 0 || len(manifest.Config.Data) != 0 || len(manifest.Layers) == 0 || len(manifest.Layers) > maxChunkCount || archiveSize <= 0 || archiveSize > transportarchive.MaxArchiveBytes {
		return e.New(e.CodeVerifyFailed, "CI carrier requires bounded raw chunks and an exact config")
	}
	remaining := archiveSize
	for _, layer := range manifest.Layers {
		if layer.MediaType != chunkMediaType || layer.Size <= 0 || layer.Size > maxChunkBytes || layer.Size > remaining || !validSHA256(layer.Digest.String()) || len(layer.URLs) != 0 || len(layer.Data) != 0 {
			return e.New(e.CodeVerifyFailed, "CI carrier chunk descriptor violates its integrity bounds")
		}
		remaining -= layer.Size
	}
	if remaining != 0 {
		return e.New(e.CodeVerifyFailed, "CI carrier chunks do not cover the exact archive size")
	}
	return nil
}

// assembleChunks writes only to private staging. No filesystem publication is
// permitted until every ordered chunk and the entire archive pass verification.
func assembleChunks(ctx context.Context, destination io.Writer, layers []v1.Layer, descriptors []v1.Descriptor, archiveDigest string) error {
	if len(layers) != len(descriptors) {
		return e.New(e.CodeVerifyFailed, "CI carrier chunk count mismatch")
	}
	whole := sha256.New()
	buffer := make([]byte, 128<<10)
	for i, layer := range layers {
		descriptor := descriptors[i]
		digest, digestErr := layer.Digest()
		size, sizeErr := layer.Size()
		mediaType, mediaErr := layer.MediaType()
		if digestErr != nil || sizeErr != nil || mediaErr != nil || digest != descriptor.Digest || size != descriptor.Size || mediaType != chunkMediaType {
			return safeIOError(ctx, e.CodeVerifyFailed, "CI carrier layer differs from its ordered descriptor")
		}
		reader, err := layer.Compressed()
		if err != nil {
			return safeIOError(ctx, e.CodeFetchFailed, "opening private CI chunk")
		}
		hash := sha256.New()
		n, copyErr := io.CopyBuffer(io.MultiWriter(destination, whole, hash), contextReader{ctx, io.LimitReader(reader, descriptor.Size+1)}, buffer)
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil {
			return safeIOError(ctx, e.CodeFetchFailed, "reading private CI chunk")
		}
		if n != descriptor.Size || hex.EncodeToString(hash.Sum(nil)) != descriptor.Digest.Hex {
			return e.New(e.CodeVerifyFailed, "private CI chunk size or digest mismatch")
		}
	}
	if "sha256:"+hex.EncodeToString(whole.Sum(nil)) != archiveDigest {
		return e.New(e.CodeVerifyFailed, "private CI archive digest mismatch")
	}
	return nil
}
