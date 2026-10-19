// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// snapshotHeldContext copies the originally collected inode without reopening
// an artifact pathname. The owning inspection operation admits capacity first
// and closes the private snapshot before releasing it; Run owns the artifact.
func (artifact *PackerArtifact) snapshotHeldContext(ctx context.Context, maxBytes int64) (*heldfile.File, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "snapshotting a Packer artifact requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "snapshotting a Packer artifact")
	}
	if artifact == nil || maxBytes <= 0 {
		return nil, e.New(e.CodeUsage, "snapshotting requires a collected artifact and positive byte budget")
	}
	if err := lockDiskInspection(ctx, &artifact.mu); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "waiting to snapshot the collected Packer artifact")
	}
	defer artifact.mu.Unlock()
	if artifact.file == nil {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact is closed")
	}
	if artifact.size <= 0 || artifact.size > maxBytes {
		return nil, e.New(e.CodeEngineCapability, "collected Packer artifact exceeds the inspection byte budget")
	}
	if !canonical.IsDigest(artifact.digest) {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact digest is not exact")
	}
	// SectionReader leaves the held descriptor's streaming cursor unchanged.
	// Compare the frozen bytes separately so allocation and read failures cannot
	// be mistaken for integrity failures.
	info, err := artifact.file.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "reading collected Packer artifact metadata")
	}
	if info.Size() != artifact.size {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact size changed")
	}
	return snapshotHeldBytes(ctx, io.NewSectionReader(artifact.file, 0, artifact.size), maxBytes, artifact.digest, artifact.size)
}

// This private copy boundary permits an observed-read cancellation test without
// changing the original-inode selection or exposing snapshot authority.
func snapshotHeldBytes(ctx context.Context, source io.Reader, maxBytes int64, digest string, size int64) (*heldfile.File, error) {
	snapshot, err := heldfile.FromReaderContext(ctx, source,
		heldfile.Options{Label: "disk-inspection-subject", MaxBytes: maxBytes})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(ctx.Err(), err), "snapshotting a Packer artifact")
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding the collected Packer artifact for inspection")
	}
	if snapshot.Digest() != digest || snapshot.Size() != size {
		return nil, e.Wrap(e.CodeVerifyFailed, snapshot.Close(), "collected Packer artifact bytes changed")
	}
	return snapshot, nil
}
