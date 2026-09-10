//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package sign

import (
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func openPublicationRegular(path string) (*os.File, os.FileInfo, error) {
	return nil, nil, e.New(e.CodeEngineCapability,
		"crash-recoverable signed-document publication cannot safely open %s on this platform", path)
}
