// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// A control document is consumed as owned bytes, so hash that bounded memory
// snapshot directly. Creating a sealed file only to read it back adds temporary
// file I/O to every cache check. Payload capabilities still use holdBlob.
func readBlob(ctx context.Context, root string, descriptor Descriptor, max int64) ([]byte, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if !isDigest(descriptor.Digest) || max <= 0 || descriptor.Size < 0 || descriptor.Size > max {
		return nil, e.New(e.CodeVerifyFailed, "OCI descriptor is malformed or exceeds its byte bound")
	}
	path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:"))
	reads, _ := ctx.Value(metadataReadsKey{}).(*metadataReads)
	if raw, ok := reads.get(path); ok {
		if int64(len(raw)) != descriptor.Size {
			return nil, e.New(e.CodeVerifyFailed, "OCI blob %s bytes do not match descriptor", descriptor.Digest)
		}
		return raw, nil
	}
	source, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "opening OCI blob %s", descriptor.Digest))
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "checking OCI blob")
	}
	if !info.Mode().IsRegular() {
		return nil, e.New(e.CodeVerifyFailed, "OCI blob %s is not a regular file", descriptor.Digest)
	}
	if info.Size() != descriptor.Size {
		return nil, e.New(e.CodeVerifyFailed, "OCI blob %s bytes do not match descriptor", descriptor.Digest)
	}
	// The extra byte detects growth after Stat without trusting the mutable path.
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: source}, descriptor.Size+1))
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading OCI blob"))
	}
	if int64(len(raw)) != descriptor.Size || digestBytes(raw) != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "OCI blob %s bytes do not match descriptor", descriptor.Digest)
	}
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	reads.put(path, raw)
	return raw, nil
}
