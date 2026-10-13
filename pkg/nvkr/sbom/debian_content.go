// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// InspectDebianPackageContent explicitly selects Debian metadata validation and
// package data-filesystem discovery through the admitted Linux inspection runtime.
// Its owner reserves capacity before capturing the bounded exact package bytes,
// checks architecture with pinned dpkg-deb, scans the exported filesystem and
// joins cleanup before returning an immutable content observation. Filename
// extensions never select this operation. DiskContentDocument can project the
// observation onto its exact archive subject; it grants no factory Run authority,
// installation acceptance or claim of exhaustive dependency coverage.
func (runtime *DiskInspectionRuntime) InspectDebianPackageContent(ctx context.Context, path, digest string, size int64, platform string) (*DiskContentInspection, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if runtime == nil || runtime.container == nil {
		return nil, e.New(e.CodeEngineCapability, "Debian package content inspection requires an admitted runtime")
	}
	observation, err := runtime.container.InspectDebianPackageContent(ctx, path, digest, size, platform)
	if err != nil {
		return nil, err
	}
	return contentFromContainerObservation(observation)
}
