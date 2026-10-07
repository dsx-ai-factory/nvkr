// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func diskContentKind(kind string) bool {
	switch kind {
	case "disk-image", "installer-iso", "installer-content", "tarball", "apt-acquisition", "host-package", "oci-image":
		return true
	default:
		return false
	}
}

// SelectionContext revalidates the admitted closure. Borrowed handles and
// attempt-private launch state never escape through this description. The
// runtime is the authenticated logical name, never the caller executable path.
func (container *DiskInspectionContainer) SelectionContext(ctx context.Context) (DiskInspectionSelection, error) {
	if _, _, _, err := container.BindingContext(ctx); err != nil {
		return DiskInspectionSelection{}, err
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return DiskInspectionSelection{}, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return DiskInspectionSelection{}, err
	}
	defer container.mu.Unlock()
	if err := container.observeBinding(ctx); err != nil {
		return DiskInspectionSelection{}, err
	}
	selection := container.selection
	selection.Workspace = nil
	selection.RuntimeExecutable = container.runtime.name
	selection.compositionKind, selection.consumerPlatform, selection.scratchVolume = "", "", ""
	return selection, nil
}

func (observation *DiskInspectionObservation) ContentBinding() (kind, guestfish, scanner, packageReader, platform string, maxBytes uint64) {
	if observation == nil {
		return "", "", "", "", "", 0
	}
	return observation.contentKind, observation.guestfish, observation.scanner, observation.packageReader, observation.platform, observation.maxFilesystemBytes
}

// InspectPackerContent and InspectPackerAcquisitionsContent retain all snapshot
// and capacity ownership in the runtime operation. Run retains the artifact.
func (container *DiskInspectionContainer) InspectPackerContent(ctx context.Context, artifact *PackerArtifact, platform string) (*DiskInspectionObservation, error) {
	return container.inspectPackerContent(ctx, artifact, "disk-image", platform)
}
func (container *DiskInspectionContainer) InspectPackerAcquisitionsContent(ctx context.Context, artifact *PackerArtifact, platform string) (*DiskInspectionObservation, error) {
	return container.inspectPackerContent(ctx, artifact, "apt-acquisition", platform)
}
func (container *DiskInspectionContainer) inspectPackerContent(ctx context.Context, artifact *PackerArtifact, kind, platform string) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "Packer content inspection"); err != nil {
		return nil, err
	}
	if container == nil || artifact == nil || (platform != "linux/amd64" && platform != "linux/arm64") {
		return nil, e.New(e.CodeEngineCapability, "content inspection requires native consumer platform and original artifact")
	}
	if err := container.validateContentOperation(kind, platform); err != nil {
		return nil, err
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	size := artifact.Size()
	if kind == "apt-acquisition" {
		size, err = artifact.acquisitionSizeContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	reservation, err := container.reserveSubject(ctx, size, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reservation.Close() }()
	var subject *heldfile.File
	if kind == "apt-acquisition" {
		subject, err = artifact.snapshotAcquisitionsContext(ctx)
	} else {
		subject, err = artifact.snapshotHeldContext(ctx, MaxDiagnosticDiskBytes)
	}
	if err != nil {
		return nil, err
	}
	return inspectAndCloseDiskSnapshot(ctx, subject,
		func(ctx context.Context, subject *heldfile.File) (*DiskInspectionObservation, error) {
			return reservation.inspectContent(ctx, subject, kind, platform)
		}, subject.Close)
}

// InspectHeldContent accepts an already-held exact producer material. Admission,
// scanning and cleanup remain one registered operation; no permit escapes.
func (container *DiskInspectionContainer) InspectHeldContent(ctx context.Context, subject *heldfile.File, kind, platform string) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "held content inspection"); err != nil {
		return nil, err
	}
	if container == nil || subject == nil || !diskContentKind(kind) || (platform != "linux/amd64" && platform != "linux/arm64") {
		return nil, e.New(e.CodeEngineCapability, "content inspection requires exact held input and supported kind/platform")
	}
	if err := container.validateContentOperation(kind, platform); err != nil {
		return nil, err
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	reservation, err := container.reserveSubject(ctx, subject.Size(), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reservation.Close() }()
	return reservation.inspectContent(ctx, subject, kind, platform)
}

// Admit the complete operation before observing or allocating a subject copy.
func (container *DiskInspectionContainer) validateContentOperation(kind, platform string) error {
	if container == nil || !diskContentKind(kind) || (platform != "linux/amd64" && platform != "linux/arm64") {
		return e.New(e.CodeEngineCapability, "unsupported disk content operation")
	}
	if container.selection.GuestfishDigest == "" || container.selection.ScannerDigest == "" || container.selection.PackageReaderDigest == "" || container.selection.MaxFilesystemBytes == 0 {
		return e.New(e.CodeEngineCapability, "disk content operation lacks its complete inspection profile")
	}
	return validateDiskInspectionSelection(container.selection)
}
