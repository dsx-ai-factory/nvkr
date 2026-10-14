// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// InspectDebianProducerContent borrows the issuing producer lifetime,
// never its pathname. The frozen instruction supplies the consumer platform.
// Factory live Run/named-output/evidence admission remains separate.
func (container *DiskInspectionContainer) InspectDebianProducerContent(ctx context.Context, archive *DebianProducerArchive) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "retained Debian producer inspection"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "retained Debian producer inspection canceled")
	}
	if archive == nil {
		return nil, e.New(e.CodeProvenanceMissing, "retained Debian producer archive is required")
	}
	owned, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return nil, err
	}
	defer finish()
	var producer ir.Node
	if err := canonical.UnmarshalExact(archive.lease.instruction, &producer); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian producer binding")
	}
	if err := validateDebianArchiveProducer(producer); err != nil {
		return nil, err
	}
	if err := validateDebianPackageInput(owned, archive.lease.digest, archive.lease.size); err != nil {
		return nil, err
	}
	observation, err := container.InspectHeldContent(owned, archive.lease.file, "host-package", producer.Debian.Platform)
	if err != nil {
		if owned.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "inspecting retained Debian archive")
		}
		return nil, err
	}
	if err := owned.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "completing retained Debian inspection")
	}
	subject, size, _, _, _ := observation.Binding()
	kind, _, _, _, platform, _ := observation.ContentBinding()
	if subject != archive.lease.digest || size != archive.lease.size || kind != "host-package" || platform != producer.Debian.Platform {
		return nil, e.New(e.CodeVerifyFailed, "retained Debian inspection changes producer bytes or target")
	}
	return observation, nil
}
