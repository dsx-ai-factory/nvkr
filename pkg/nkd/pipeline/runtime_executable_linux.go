//go:build linux

package pipeline

import (
	"context"
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const maxQualifiedRuntimeBytes int64 = 1 << 30

var qualifiedRuntimeEnvironment = []string{
	"HOME=/dev/null",
	"PATH=/nonexistent",
	"LANG=C",
	"LC_ALL=C",
	"TZ=UTC",
	"DOCKER_CONFIG=/dev/null",
	"DOCKER_HOST=" + qualifiedRuntimeEndpoint,
}

// snapshotRuntimeExecutable copies the resolved runtime inode into the shared
// sealed-held-file boundary. There is no snapshot pathname to rename or replace;
// qualified observation and execution inherit this descriptor directly.
func snapshotRuntimeExecutable(runtimePath string) (*heldRuntimeExecutable, error) {
	return snapshotRuntimeExecutableContext(context.Background(), runtimePath)
}

func snapshotRuntimeExecutableContext(ctx context.Context, runtimePath string) (*heldRuntimeExecutable, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "holding a container runtime requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "holding container runtime %s", runtimePath)
	}
	resolved, err := filepath.EvalSymlinks(runtimePath)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "resolving container runtime %s", runtimePath)
	}
	capability, err := heldfile.OpenContext(ctx, resolved, heldfile.Options{
		Label: "qualified-runtime", Executable: true, MaxBytes: maxQualifiedRuntimeBytes,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "holding container runtime %s", runtimePath)
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding container runtime %s", runtimePath)
	}
	return &heldRuntimeExecutable{
		capability: capability, endpoint: qualifiedRuntimeEndpoint,
		environment: append([]string(nil), qualifiedRuntimeEnvironment...),
	}, nil
}
