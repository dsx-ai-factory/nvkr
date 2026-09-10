//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sign

import (
	"context"
	"errors"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func acquirePublicationLock(ctx context.Context, path string) (*os.File, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err,
			"opening signed-document publication lock %s", path)
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, e.New(e.CodeWriteConflict,
			"signed-document publication lock %s is not an owner-only regular file", path)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, e.Wrap(e.CodeWriteConflict, err,
				"signed-document publication is already active for %s", path)
		}
		return nil, e.Wrap(e.CodeWriteConflict, err,
			"locking signed-document publication %s", path)
	}
	return file, nil
}
