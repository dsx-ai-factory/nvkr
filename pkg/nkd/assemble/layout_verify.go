// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const maxOCILayoutBlobs = 100_000

// VerifyLayoutContext re-checks every blob in an OCI image layout through held
// directory and regular-file descriptors. Each blob's SHA-256 must equal its own
// filename, so corruption of the layer or config payload is caught without reading
// an attacker-sized blob into memory or following a blob symlink.
func VerifyLayoutContext(ctx context.Context, dir string) error {
	if err := checkContext(ctx, "verifying an OCI layout"); err != nil {
		return err
	}
	root, err := openLayoutDirectory(unix.AT_FDCWD, dir, "OCI layout")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	blobs, err := openLayoutDirectory(int(root.Fd()), "blobs", "OCI blobs directory")
	if err != nil {
		return err
	}
	defer func() { _ = blobs.Close() }()
	digests, err := openLayoutDirectory(int(blobs.Fd()), "sha256", "OCI sha256 directory")
	if err != nil {
		return err
	}
	defer func() { _ = digests.Close() }()

	listed, err := readLayoutEntries(ctx, digests)
	if err != nil {
		return err
	}
	if len(listed) == 0 {
		return e.New(e.CodeVerifyFailed, "oci layout %s has no blobs", dir)
	}
	for _, entry := range listed {
		if err := checkContext(ctx, "verifying OCI layout blobs"); err != nil {
			return err
		}
		name := entry.Name()
		if !entry.Mode().IsRegular() || !canonical.IsDigest("sha256:"+name) {
			return e.New(e.CodeVerifyFailed, "OCI blob %q is not a canonically named regular file", name)
		}
		fd, err := unix.Openat(int(digests.Fd()), name,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening OCI blob %s without following links", name)
		}
		blob := os.NewFile(uintptr(fd), name)
		if blob == nil {
			_ = unix.Close(fd)
			return e.New(e.CodeInternal, "holding OCI blob %s", name)
		}
		before, statErr := blob.Stat()
		if statErr != nil || !before.Mode().IsRegular() || !os.SameFile(entry, before) {
			_ = blob.Close()
			if statErr != nil {
				return e.Wrap(e.CodeVerifyFailed, statErr, "inspecting OCI blob %s", name)
			}
			return e.New(e.CodeVerifyFailed, "OCI blob %s changed while opening", name)
		}
		hash := sha256.New()
		_, copyErr := io.CopyBuffer(hash, assembleContextReader{ctx: ctx, source: blob}, make([]byte, 128<<10))
		after, afterErr := blob.Stat()
		closeErr := blob.Close()
		if copyErr != nil {
			if ctx.Err() != nil {
				return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying OCI blob %s", name)
			}
			return e.Wrap(e.CodeVerifyFailed, copyErr, "hashing OCI blob %s", name)
		}
		if afterErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
			before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
			if afterErr != nil {
				return e.Wrap(e.CodeVerifyFailed, afterErr, "re-inspecting OCI blob %s", name)
			}
			return e.New(e.CodeVerifyFailed, "OCI blob %s changed while hashing", name)
		}
		if closeErr != nil {
			return e.Wrap(e.CodeVerifyFailed, closeErr, "closing OCI blob %s", name)
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != name {
			return e.New(e.CodeVerifyFailed, "oci blob %s corrupted (content hash mismatch)", name)
		}
	}

	if _, err := digests.Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rewinding OCI blob directory")
	}
	after, err := readLayoutEntries(ctx, digests)
	if err != nil {
		return err
	}
	if !sameLayoutEntries(listed, after) {
		return e.New(e.CodeVerifyFailed, "OCI blob directory changed while verifying")
	}
	return checkContext(ctx, "verifying an OCI layout")
}

func openLayoutDirectory(parentFD int, name, label string) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_NONBLOCK
	var fd int
	var err error
	if parentFD == unix.AT_FDCWD {
		fd, err = unix.Open(name, flags, 0)
	} else {
		fd, err = unix.Openat(parentFD, name, flags, 0)
	}
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening %s without following links", label)
	}
	directory := os.NewFile(uintptr(fd), name)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeInternal, "holding %s", label)
	}
	return directory, nil
}

func readLayoutEntries(ctx context.Context, directory *os.File) ([]os.FileInfo, error) {
	entries := make([]os.FileInfo, 0, 64)
	for {
		if err := checkContext(ctx, "enumerating OCI layout blobs"); err != nil {
			return nil, err
		}
		batch, err := directory.Readdir(1024)
		if len(entries)+len(batch) > maxOCILayoutBlobs {
			return nil, e.New(e.CodeVerifyFailed, "OCI layout exceeds its %d-blob bound", maxOCILayoutBlobs)
		}
		entries = append(entries, batch...)
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "enumerating OCI layout blobs")
		}
	}
}

func sameLayoutEntries(left, right []os.FileInfo) bool {
	if len(left) != len(right) {
		return false
	}
	byName := make(map[string]os.FileInfo, len(left))
	for _, entry := range left {
		byName[entry.Name()] = entry
	}
	if len(byName) != len(left) {
		return false
	}
	for _, entry := range right {
		before, ok := byName[entry.Name()]
		if !ok || !os.SameFile(before, entry) || before.Size() != entry.Size() ||
			before.Mode() != entry.Mode() || !before.ModTime().Equal(entry.ModTime()) {
			return false
		}
	}
	return true
}
