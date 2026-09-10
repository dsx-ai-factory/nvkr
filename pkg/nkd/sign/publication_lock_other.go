//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package sign

import (
	"context"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func acquirePublicationLock(ctx context.Context, _ string) (*os.File, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return nil, e.New(e.CodeEngineCapability,
		"crash-recoverable signed-document publication requires a kernel advisory lock")
}
