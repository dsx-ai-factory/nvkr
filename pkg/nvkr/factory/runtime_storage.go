// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// Complete-Plan adapters do not yet project a storage override to every
// producer, utility and runner runtime. A frozen override must not be ignored.
// The separate held inspector and direct-Packer owners support typed selection.
func rejectUnsupportedRuntimeStorage(plan *Plan, adapter string) error {
	if driver := plan.Environment.Runtime.PodmanStorageDriver; driver != "" {
		return e.New(e.CodeEngineCapability,
			"%s cannot enforce Environment.runtime.podmanStorageDriver %q across the complete Plan", adapter, driver)
	}
	return nil
}
