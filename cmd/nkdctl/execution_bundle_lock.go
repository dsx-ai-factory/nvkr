package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const executionBundleLockRetryInterval = 20 * time.Millisecond

func executionBundleContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

// acquireExecutionBundleLock serializes cooperating writers through a persistent
// owner-only inode. Process exit releases the kernel lock, so a crash leaves a
// safely reusable path instead of an O_EXCL sentinel that needs stale-owner
// guessing. Non-blocking attempts keep lock acquisition context-cancelable.
func acquireExecutionBundleLock(ctx context.Context, path string) (func(), error) {
	if err := executionBundleContextError(ctx, "acquiring execution bundle commit lock"); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err,
			"opening execution bundle commit lock %s", path)
	}
	lock := os.NewFile(uintptr(fd), path)
	if lock == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeInternal, "opening execution bundle commit lock %s", path)
	}
	closeLock := func() {
		_ = lock.Close()
	}
	info, err := lock.Stat()
	if err != nil {
		closeLock()
		return nil, e.Wrap(e.CodeWriteConflict, err,
			"inspecting execution bundle commit lock %s", path)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		closeLock()
		return nil, e.New(e.CodeWriteConflict,
			"execution bundle commit lock %s must be a regular owner-only file", path)
	}

	for {
		if err := executionBundleContextError(ctx, "waiting for execution bundle commit lock"); err != nil {
			closeLock()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeLock()
			return nil, e.Wrap(e.CodeWriteConflict, err,
				"locking execution bundle commit lock %s", path)
		}
		timer := time.NewTimer(executionBundleLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			closeLock()
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(),
				"waiting for execution bundle commit lock %s", path)
		case <-timer.C:
		}
	}

	release := func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = lock.Close()
	}
	failLocked := func(err error) (func(), error) {
		release()
		return nil, err
	}
	if err := executionBundleContextError(ctx, "acquiring execution bundle commit lock"); err != nil {
		return failLocked(err)
	}
	if err := lock.Truncate(0); err != nil {
		return failLocked(e.Wrap(e.CodeInternal, err,
			"clearing execution bundle commit lock metadata"))
	}
	if _, err := lock.Seek(0, io.SeekStart); err != nil {
		return failLocked(e.Wrap(e.CodeInternal, err,
			"seeking execution bundle commit lock metadata"))
	}
	if _, err := fmt.Fprintf(lock, "pid=%d\n", os.Getpid()); err != nil {
		return failLocked(e.Wrap(e.CodeInternal, err,
			"recording execution bundle commit lock owner"))
	}
	if err := lock.Sync(); err != nil {
		return failLocked(e.Wrap(e.CodeInternal, err,
			"syncing execution bundle commit lock"))
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return failLocked(e.Wrap(e.CodeInternal, err,
			"opening execution bundle lock directory"))
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return failLocked(e.Wrap(e.CodeInternal, syncErr,
			"syncing execution bundle lock directory"))
	}
	if closeErr != nil {
		return failLocked(e.Wrap(e.CodeInternal, closeErr,
			"closing execution bundle lock directory"))
	}
	if err := executionBundleContextError(ctx, "acquiring execution bundle commit lock"); err != nil {
		return failLocked(err)
	}
	return release, nil
}
