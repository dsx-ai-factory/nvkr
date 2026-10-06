//go:build !linux

package execution

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func verifyCurrentCgroupLimits(context.Context, uint64, uint64, uint64) error {
	return e.New(e.CodeEngineCapability, "container limit verification requires Linux cgroup v2")
}
