package execution

import (
	"context"
	"math"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// VerifyCurrentCgroupLimits verifies the current process's enforced container
// ceilings, not available capacity or a resource reservation. Each current-group
// limit must exactly realize the requested value; stricter visible ancestors also
// reject. Namespace-hidden ancestors and allocatable headroom are not observed.
// The values must come from the owning executor's frozen inspection selection.
func VerifyCurrentCgroupLimits(ctx context.Context, cpuMillis, memoryMiB, pids uint64) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "container limit verification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "container limit verification canceled")
	}
	if cpuMillis == 0 || memoryMiB == 0 || memoryMiB > math.MaxUint64/(1024*1024) || pids == 0 {
		return e.New(e.CodeEngineCapability, "container CPU, memory and PID ceilings must be positive and representable")
	}
	return verifyCurrentCgroupLimits(ctx, cpuMillis, memoryMiB, pids)
}
