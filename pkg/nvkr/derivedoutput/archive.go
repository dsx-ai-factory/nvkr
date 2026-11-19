// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package derivedoutput

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// InventoryArchive emits the one fixed, non-executable inventory member. A
// qualifier checks the canonical tar payload independently of gzip compression.
func InventoryArchive(ctx context.Context, inventory []byte) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "inventory archive requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inventory derivation canceled")
	}
	if len(inventory) == 0 || len(inventory) > 8<<20 {
		return nil, e.New(e.CodeEngineCapability, "derived inventory exceeds admitted document size")
	}
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "artifacts/inventory.yaml", Mode: 0o644, Size: int64(len(inventory)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding inventory archive header")
	}
	if _, err := archive.Write(inventory); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding inventory archive")
	}
	if err := archive.Close(); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "closing inventory archive")
	}
	if err := compressed.Close(); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "closing compressed inventory")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inventory derivation canceled")
	}
	return buffer.Bytes(), nil
}

// ValidateInventoryArchive compares the sole regular inventory member and its
// canonical tar envelope, accepting gzip implementations with different output.
// Bounds apply before decompression; concatenated streams and trailers fail.
func ValidateInventoryArchive(ctx context.Context, raw, inventory []byte) error {
	canonical, err := InventoryArchive(ctx, inventory)
	if err != nil {
		return err
	}
	uncompressed := func(raw []byte) ([]byte, error) {
		if len(raw) > 9<<20 {
			return nil, e.New(e.CodeVerifyFailed, "inventory archive exceeds its transport bound")
		}
		input := bytes.NewReader(raw)
		reader, err := gzip.NewReader(input)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "opening inventory gzip")
		}
		reader.Multistream(false)
		defer func() { _ = reader.Close() }()
		payload, err := io.ReadAll(io.LimitReader(reader, (8<<20)+4097))
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading complete inventory gzip")
		}
		if len(payload) > (8<<20)+4096 || input.Len() != 0 {
			return nil, e.New(e.CodeVerifyFailed, "inventory archive has oversized or trailing content")
		}
		return payload, nil
	}
	expected, err := uncompressed(canonical)
	if err != nil {
		return err
	}
	actual, err := uncompressed(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return e.New(e.CodeVerifyFailed, "signed inventory differs from final Release re-projection")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inventory verification canceled")
	}
	return nil
}
