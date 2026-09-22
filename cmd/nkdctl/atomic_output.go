package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// writeAtomic writes and syncs data in the target directory, checks cancellation
// immediately before the rename linearization point, then renames and syncs the
// parent. A failure before rename leaves no public partial file; once rename begins,
// durability completion is intentionally non-cancelable.
func writeAtomic(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "atomic output requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nkd-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating temp for %s", path)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return e.Wrap(e.CodeInternal, err, "chmod temp for %s", path)
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "writing %s", path)
		}
		end := min(offset+(128<<10), len(data))
		written, err := tmp.Write(data[offset:end])
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "writing temp for %s", path)
		}
		if written == 0 {
			return e.Wrap(e.CodeInternal, io.ErrShortWrite, "writing temp for %s", path)
		}
		offset += written
	}
	if err := tmp.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "syncing temp for %s", path)
	}
	if err := tmp.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing temp for %s", path)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "publishing %s", path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return e.Wrap(e.CodeInternal, err, "renaming temp onto %s", path)
	}
	cleanup = false
	directory, err := os.Open(dir)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening parent directory for %s", path)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return e.Wrap(e.CodeInternal, err, "syncing parent directory for %s", path)
	}
	return nil
}
