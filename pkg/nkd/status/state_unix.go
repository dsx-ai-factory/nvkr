//go:build linux || darwin

package status

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func admitStatusState() error { return nil }

func readProtectedHighWaterContext(ctx context.Context, path string, destination *highWaterFile) ([]byte, error) {
	raw, err := readProtectedHighWaterBytesContext(ctx, path, e.CodeStatusCheckpoint)
	if err != nil {
		return nil, err
	}
	if err := canonical.UnmarshalExact(raw, destination); err != nil {
		return nil, e.Wrap(e.CodeStatusCheckpoint, err, "decoding protected status high-water %s", path)
	}
	return raw, nil
}

func readProtectedHighWaterBytesContext(ctx context.Context, path string, code e.Code) ([]byte, error) {
	if err := statusContextError(ctx, "reading protected status high-water"); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, e.Wrap(code, err, "opening protected status high-water %s without following links", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, e.New(code, "opening protected status high-water capability %s", path)
	}
	before, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, e.Wrap(code, err, "inspecting protected status high-water %s", path)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || before.Size() > 1<<20 {
		_ = file.Close()
		return nil, e.New(code,
			"status high-water %s must be a regular owner-only file (mode 0600 or stricter) no larger than 1 MiB", path)
	}
	raw, readErr := io.ReadAll(io.LimitReader(statusContextReader{ctx: ctx, source: file}, (1<<20)+1))
	after, statErr := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	closeErr := file.Close()
	if readErr != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading protected status high-water %s", path)
		}
		return nil, e.Wrap(code, readErr, "reading protected status high-water %s", path)
	}
	if statErr != nil {
		return nil, e.Wrap(code, statErr, "rechecking protected status high-water %s", path)
	}
	if pathErr != nil {
		return nil, e.Wrap(code, pathErr, "rechecking protected status high-water path %s", path)
	}
	if closeErr != nil {
		return nil, e.Wrap(code, closeErr, "closing protected status high-water %s", path)
	}
	if len(raw) > 1<<20 || int64(len(raw)) != before.Size() ||
		!after.Mode().IsRegular() || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(before, after) || !os.SameFile(before, pathAfter) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) ||
		after.Mode() != before.Mode() {
		return nil, e.New(code, "status high-water %s changed while reading", path)
	}
	if err := statusContextError(ctx, "reading protected status high-water"); err != nil {
		return nil, err
	}
	return raw, nil
}

// persistStatusHighWaterContext commits only against the protected bytes evaluated
// before verification. The persistent lock inode is kernel-released on process death.
func persistStatusHighWaterContext(ctx context.Context, path string, observed, next []byte) error {
	if err := statusContextError(ctx, "persisting status high-water"); err != nil {
		return err
	}
	if len(observed) > 1<<20 || len(next) > 1<<20 {
		return e.New(e.CodeStatusCheckpoint, "status high-water exceeds its 1 MiB bound")
	}
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".nkd-status-high-water-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "staging status high-water %s", path)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return e.Wrap(e.CodeInternal, err, "protecting staged status high-water")
	}
	if _, err := io.CopyBuffer(temporary, statusContextReader{ctx: ctx, source: bytes.NewReader(next)}, make([]byte, 128<<10)); err != nil {
		_ = temporary.Close()
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "writing staged status high-water")
		}
		return e.Wrap(e.CodeInternal, err, "writing staged status high-water")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return e.Wrap(e.CodeInternal, err, "syncing staged status high-water")
	}
	if err := statusContextError(ctx, "staging status high-water"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing staged status high-water")
	}

	lease, err := acquireStatusHighWaterLockContext(ctx, path+".lease")
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(int(lease.Fd()), unix.LOCK_UN)
		_ = lease.Close()
	}()
	current, err := readProtectedHighWaterBytesContext(ctx, path, e.CodeWriteConflict)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, observed) {
		return e.New(e.CodeWriteConflict, "status high-water changed while checkpoint verification was in progress")
	}
	if err := statusContextError(ctx, "committing status high-water"); err != nil {
		return err
	}
	// Rename is the publication commit point. Once it succeeds, complete directory
	// durability and report the committed result even if cancellation arrives.
	if err := os.Rename(temporaryPath, path); err != nil {
		return e.Wrap(e.CodeInternal, err, "committing status high-water")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening status high-water directory")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return e.Wrap(e.CodeInternal, syncErr, "syncing status high-water directory")
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing status high-water directory")
	}
	return nil
}

func acquireStatusHighWaterLockContext(ctx context.Context, path string) (*os.File, error) {
	if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err, "opening status high-water advisory lock %s", path)
	}
	lease := os.NewFile(uintptr(fd), path)
	if lease == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeInternal, "opening status high-water advisory lock %s", path)
	}
	closeWith := func(err error) (*os.File, error) {
		_ = lease.Close()
		return nil, err
	}
	info, err := lease.Stat()
	if err != nil {
		return closeWith(e.Wrap(e.CodeWriteConflict, err, "inspecting status high-water advisory lock %s", path))
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return closeWith(e.New(e.CodeWriteConflict,
			"status high-water advisory lock %s must be a regular owner-only file", path))
	}
	for {
		if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
			return closeWith(err)
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeWith(e.New(e.CodeWriteConflict,
				"another status high-water writer holds advisory lock %s", path))
		}
		return closeWith(e.Wrap(e.CodeWriteConflict, err,
			"locking status high-water advisory lock %s", path))
	}
	if err := lease.Sync(); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, err, "syncing status high-water advisory lock"))
	}
	if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(err)
	}
	// Persist the reusable lock inode itself. Subsequent crashes release only the
	// kernel lock; they never require deleting this directory entry.
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, err, "opening status high-water lock directory"))
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, syncErr, "syncing status high-water lock directory"))
	}
	if closeErr != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, closeErr, "closing status high-water lock directory"))
	}
	return lease, nil
}
