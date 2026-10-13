// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// MaxDebianPackageBytes is the input limit of the existing pinned package reader.
const MaxDebianPackageBytes int64 = 1 << 30

// InspectDebianPackageContent owns capacity admission, an exact held snapshot,
// the offline content operation and cleanup. The path and expected digest/size
// describe input; only the returned observation binds the bytes actually read.
// The consumer platform is independent of the native inspection host platform.
// This operation does not authenticate a producer result or authorize a Run.
func (container *DiskInspectionContainer) InspectDebianPackageContent(ctx context.Context, path, digest string, size int64, platform string) (*DiskInspectionObservation, error) {
	if err := validateDebianPackageInput(ctx, digest, size); err != nil {
		return nil, err
	}
	if err := container.validateContentOperation("host-package", platform); err != nil {
		return nil, err
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	reservation, err := container.reserveSubject(ctx, size, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reservation.Close() }()
	subject, err := captureDebianPackage(ctx, path, digest, size)
	if err != nil {
		return nil, err
	}
	return inspectAndCloseDiskSnapshot(ctx, subject,
		func(ctx context.Context, subject *heldfile.File) (*DiskInspectionObservation, error) {
			return reservation.inspectContent(ctx, subject, "host-package", platform)
		}, subject.Close)
}

func validateDebianPackageInput(ctx context.Context, digest string, size int64) error {
	if err := requireRunContext(ctx, "Debian package content inspection"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "Debian package content inspection canceled")
	}
	if !canonical.IsDigest(digest) {
		return e.New(e.CodeUnverifiedRef, "Debian package inspection requires an exact archive digest")
	}
	if size <= 0 || size > MaxDebianPackageBytes {
		return e.New(e.CodeEngineCapability, "Debian package inspection requires a positive archive size within its byte budget")
	}
	return nil
}

// Capture establishes byte identity only. Debian format and target architecture
// are checked later by the admitted pinned reader, never by the filename.
func captureDebianPackage(ctx context.Context, path, digest string, size int64) (*heldfile.File, error) {
	if err := validateDebianPackageInput(ctx, digest, size); err != nil {
		return nil, err
	}
	subject, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "debian-package", MaxBytes: size, ExpectedDigest: digest})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(err, ctx.Err()), "capturing Debian package")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "capturing Debian package")
	}
	if subject.Size() != size {
		err := e.New(e.CodeVerifyFailed, "Debian package length differs from expected bytes")
		return nil, e.Wrap(e.CodeVerifyFailed, errors.Join(err, subject.Close()), "checking captured Debian package")
	}
	return subject, nil
}
