// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// PodmanStorageDriver is frozen in the Environment; empty preserves the
// runtime's existing storage selection. Ambient STORAGE_DRIVER is never used.
type PodmanStorageDriver string

const (
	PodmanStorageVFS     PodmanStorageDriver = "vfs"
	PodmanStorageOverlay PodmanStorageDriver = "overlay"
)

func (driver PodmanStorageDriver) Validate(runtimeName string) error {
	if driver == "" {
		return nil
	}
	if runtimeName != "" && runtimeName != "podman" {
		return e.New(e.CodeInvalidCatalog, "Podman storage driver requires the Podman runtime")
	}
	if driver != PodmanStorageVFS && driver != PodmanStorageOverlay {
		return e.New(e.CodeInvalidCatalog, "unsupported Podman storage driver %q (vfs|overlay)", driver)
	}
	return nil
}
