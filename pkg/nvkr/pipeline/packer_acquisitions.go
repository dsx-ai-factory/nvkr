// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

// ObserveAcquisitionsContext observes only the package carrier collected by the
// same producer invocation as this still-open disk. No caller-selected path or
// externally supplied observation can be attached to the artifact. The result
// authenticates byte correspondence, not repository origin or complete coverage.
func (artifact *PackerArtifact) ObserveAcquisitionsContext(ctx context.Context) (*material.AcquisitionObservation, error) {
	if ctx == nil || artifact == nil {
		return nil, e.New(e.CodeUsage, "observing acquisitions requires context and a held Packer artifact")
	}
	if err := lockDiskInspection(ctx, &artifact.mu); err != nil {
		return nil, err
	}
	defer artifact.mu.Unlock()
	if artifact.file == nil || artifact.acquisitions == nil {
		return nil, e.New(e.CodeGateUnmet, "disk has no open producer-owned acquisition carrier")
	}
	return material.ObserveAcquisitionArchive(ctx, artifact.acquisitions)
}

// Size is only a capacity description of this original retained carrier. It is
// not an acquisition observation: the later snapshot authenticates exact bytes,
// and the material/helper owners still validate their complete archive content.
func (artifact *PackerArtifact) acquisitionSizeContext(ctx context.Context) (int64, error) {
	if ctx == nil || artifact == nil {
		return 0, e.New(e.CodeUsage, "acquisition sizing requires context and original artifact")
	}
	if err := lockDiskInspection(ctx, &artifact.mu); err != nil {
		return 0, err
	}
	defer artifact.mu.Unlock()
	if artifact.file == nil || artifact.acquisitions == nil {
		return 0, e.New(e.CodeGateUnmet, "original disk has no retained acquisitions")
	}
	return artifact.acquisitions.Size(), nil
}

// snapshotAcquisitionsContext copies the original collected carrier. Only the
// runtime's registered operation admits capacity and closes this private copy.
func (artifact *PackerArtifact) snapshotAcquisitionsContext(ctx context.Context) (*heldfile.File, error) {
	if ctx == nil || artifact == nil {
		return nil, e.New(e.CodeUsage, "acquisition snapshot requires context and original artifact")
	}
	if err := lockDiskInspection(ctx, &artifact.mu); err != nil {
		return nil, err
	}
	defer artifact.mu.Unlock()
	if artifact.file == nil || artifact.acquisitions == nil {
		return nil, e.New(e.CodeGateUnmet, "original disk has no retained acquisitions")
	}
	if err := artifact.acquisitions.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking original acquisitions")
	}
	return heldfile.FromReaderContext(ctx, io.NewSectionReader(artifact.acquisitions, 0, artifact.acquisitions.Size()), heldfile.Options{Label: "same-attempt-acquisitions", MaxBytes: artifact.acquisitions.Size(), ExpectedDigest: artifact.acquisitions.Digest()})
}

// Collection uses the existing bounded Docker-copy archive adapter. The outer
// transport contains exactly one declared file; the material owner validates the
// inner capture without extraction. Both handles are closed on every failure.
func collectPackerContainerAcquisitions(ctx context.Context, runtime packerContainerRuntime,
	container, sourcePath, root string, budget execution.ResourceRequest, observed execution.ResourceCapacity, diskBytes int64,
) (capture *heldfile.File, returnErr error) {
	limit, err := packerAcquisitionCollectionLimit(budget, observed, diskBytes)
	if err != nil {
		return nil, err
	}
	transportPath := filepath.Join(root, "acquisitions-transport.tar")
	file, err := os.OpenFile(transportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "creating acquisition transport")
	}
	stderr := newPackerValidationOutput(nil)
	runErr := runtime.Run(ctx, nil, &packerContainerLimitWriter{writer: file, remaining: limit}, stderr,
		"cp", container+":"+sourcePath, "-")
	if err := errors.Join(runErr, file.Sync(), file.Close()); err != nil {
		return nil, runtimeCommandError(ctx, err, stderr.buffer.String(), e.CodeVerifyFailed, "collecting declared acquisition carrier")
	}
	transport, err := heldfile.OpenContext(ctx, transportPath, heldfile.Options{Label: "acquisition-transport", MaxBytes: limit})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding acquisition transport")
	}
	defer func() {
		if err := transport.Close(); err != nil {
			if capture != nil {
				err = errors.Join(err, capture.Close())
				capture = nil
			}
			returnErr = e.Wrap(e.CodeInternal, errors.Join(returnErr, err), "closing acquisition transport")
		}
	}()
	if err := os.Remove(transportPath); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "unlinking acquisition transport")
	}
	name := path.Base(sourcePath)
	output := filepath.Join(root, "acquisition-carrier")
	if err := transportarchive.UnpackOpen(ctx, transport.OSFile(), output, []string{name}); err != nil {
		return nil, err
	}
	if err := transport.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking acquisition transport")
	}
	capture, err = heldfile.OpenContext(ctx, filepath.Join(output, name), heldfile.Options{Label: "producer-acquisition-carrier", MaxBytes: limit})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding producer acquisition carrier")
	}
	if _, err := material.ObserveAcquisitionArchive(ctx, capture); err != nil {
		return nil, errors.Join(err, capture.Close())
	}
	return capture, nil
}

// Outer transport and inner capture can coexist as sealed RAM-backed files on
// Linux. Reserve 64 MiB for bounded manifest decoding and bookkeeping, then
// account for both copies separately from the three scratch copies and the
// already-held disk. Observed workspace free space already excludes that disk;
// subtract it only from the declared total. Neither observation nor this budget
// is a kernel reservation.
func packerAcquisitionCollectionLimit(budget execution.ResourceRequest, observed execution.ResourceCapacity, diskBytes int64) (int64, error) {
	const metadataMiB = 64
	memory := min(budget.MemoryMiB, observed.MemoryMiB)
	if diskBytes <= 0 || memory <= metadataMiB {
		return 0, e.New(e.CodeEngineCapability, "acquisition collection has no admitted disk or metadata memory budget")
	}
	diskMiB := uint64((diskBytes-1)/(1<<20)) + 1
	if budget.EphemeralStorageMiB <= diskMiB {
		return 0, e.New(e.CodeEngineCapability, "no admitted scratch capacity remains for acquisitions")
	}
	storage := min(budget.EphemeralStorageMiB-diskMiB, observed.EphemeralStorageMiB)
	limit := min(packerArtifactArchiveLimit(storage), material.MaxAcquisitionArchiveBytes)
	memoryMiB := memory - metadataMiB
	if memoryMiB <= uint64(math.MaxInt64>>20) {
		limit = min(limit, (int64(memoryMiB)<<20)/2)
	}
	if limit < 2048 {
		return 0, e.New(e.CodeEngineCapability, "acquisition collection cannot fit a bounded archive")
	}
	return limit, nil
}
