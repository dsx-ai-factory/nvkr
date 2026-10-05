//go:build linux

package execution

import (
	"context"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func verifyCurrentCgroupLimits(ctx context.Context, cpuMillis, memoryMiB, pids uint64) error {
	leaf, root, err := currentCgroupV2Location()
	if err != nil {
		return err
	}
	return verifyCgroupLimits(ctx, leaf, root, cpuMillis, memoryMiB, pids)
}

// Membership and the cgroup2 mount/device are authenticated by the existing
// kernel observation owner. This private operation also supports filesystem
// fixtures; caller paths cannot reach the public admission operation.
func verifyCgroupLimits(ctx context.Context, leaf, root string, cpuMillis, memoryMiB, pids uint64) error {
	ancestry, err := cgroupV2Ancestry(leaf, root)
	if err != nil {
		return err
	}
	for _, directory := range ancestry {
		for _, control := range []string{"cpu.max", "memory.max", "pids.max"} {
			if err := ctx.Err(); err != nil {
				return e.Wrap(e.CodeEngineCapability, err, "container limit verification canceled")
			}
			raw, err := readBoundedKernelText(filepath.Join(directory, control), 128)
			// The hierarchy root can omit controllers, but the current group
			// must expose all three explicit, finite enforcement limits.
			if os.IsNotExist(err) && directory == root && directory != leaf {
				continue
			}
			if err != nil {
				return e.Wrap(e.CodeEngineCapability, err, "reading enforced container %s", control)
			}
			if control == "cpu.max" {
				capacity, limited, err := parseCPUMax(raw)
				if err != nil {
					return e.Wrap(e.CodeEngineCapability, err, "reading enforced container CPU ceiling")
				}
				if directory != leaf {
					if limited && capacity < cpuMillis {
						return e.New(e.CodeEngineCapability, "ancestor CPU ceiling is below the inspection grant")
					}
					continue
				}
				if !limited {
					return e.New(e.CodeEngineCapability, "inspection container CPU ceiling is unlimited")
				}
				// parseCPUMax validates the kernel's two scalar fields, but
				// returns floor capacity. Compare the exact ratio here so a
				// fractional excess cannot masquerade as the requested cap.
				fields := strings.Fields(raw)
				quota, _ := strconv.ParseUint(fields[0], 10, 64)
				period, _ := strconv.ParseUint(fields[1], 10, 64)
				actualHigh, actualLow := bits.Mul64(quota, 1000)
				wantHigh, wantLow := bits.Mul64(cpuMillis, period)
				if actualHigh != wantHigh || actualLow != wantLow {
					return e.New(e.CodeEngineCapability, "inspection container CPU ceiling does not match its frozen grant")
				}
				continue
			}
			value := strings.TrimSpace(raw)
			if value == "max" && directory != leaf {
				continue
			}
			ceiling, err := strconv.ParseUint(value, 10, 64)
			if err != nil || ceiling == 0 {
				return e.New(e.CodeEngineCapability, "inspection container %s ceiling is absent, unlimited or malformed", control)
			}
			want := pids
			if control == "memory.max" {
				want = memoryMiB * bytesPerMiB // public admission checks overflow
			}
			if (directory == leaf && ceiling != want) || ceiling < want {
				return e.New(e.CodeEngineCapability, "inspection container %s ceiling does not realize its frozen grant", control)
			}
		}
	}
	return nil
}
