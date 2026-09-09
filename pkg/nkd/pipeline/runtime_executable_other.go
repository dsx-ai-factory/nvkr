//go:build !linux

package pipeline

import (
	"context"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func snapshotRuntimeExecutable(runtimePath string) (*heldRuntimeExecutable, error) {
	return snapshotRuntimeExecutableContext(context.Background(), runtimePath)
}

func snapshotRuntimeExecutableContext(ctx context.Context, runtimePath string) (*heldRuntimeExecutable, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "holding a container runtime requires a non-nil context")
	}
	return nil, e.New(e.CodeEngineCapability,
		"qualified local execution requires Linux sealed anonymous executable descriptors; runtime %s cannot be qualified on this platform",
		runtimePath)
}
