//go:build !linux && !darwin

package status

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func admitStatusState() error {
	return e.New(e.CodeEngineCapability, "protected status state requires Linux or macOS file and lock semantics")
}

func readProtectedHighWaterContext(context.Context, string, *highWaterFile) ([]byte, error) {
	return nil, admitStatusState()
}

func persistStatusHighWaterContext(context.Context, string, []byte, []byte) error {
	return admitStatusState()
}
