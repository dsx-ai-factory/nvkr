// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"math"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// VerifyCurrentCgroupLimits verifies the current process's enforced container
// ceilings, not available capacity or a resource reservation. Each current-group
// limit must exactly realize the requested value; stricter visible ancestors also
// reject. Namespace-hidden ancestors and allocatable headroom are not observed.
// The values must come from the owning executor's frozen inspection selection.
func VerifyCurrentCgroupLimits(ctx context.Context, cpuMillis, memoryMiB, pids uint64) error {
	if err := localObservationContextError(ctx, "container limit verification"); err != nil {
		return err
	}
	if cpuMillis == 0 || memoryMiB == 0 || memoryMiB > math.MaxUint64/(1024*1024) || pids == 0 {
		return e.New(e.CodeEngineCapability, "container CPU, memory and PID ceilings must be positive and representable")
	}
	return verifyCurrentCgroupLimits(ctx, cpuMillis, memoryMiB, pids)
}
