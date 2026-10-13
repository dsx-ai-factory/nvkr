// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package securepath owns descriptor-relative filesystem access shared by
// execution snapshots and transport archives. Returned handles belong to callers;
// these observations do not confer artifact or release authority.
package securepath

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// CreateParentBeneath holds a descriptor for the final path's parent while
// every intermediate directory is resolved with O_NOFOLLOW. Creation uses mkdirat
// relative to the already-authenticated parent, so a swapped workspace path cannot
// redirect a download outside the runner-owned root.
func CreateParentBeneath(root, relative string) (*os.File, string, error) {
	rootFile, _, err := OpenDirectory(root)
	if err != nil {
		return nil, "", fmt.Errorf("opening trusted artifact root %s: %w", root, err)
	}
	defer rootFile.Close()
	return CreateParent(rootFile, relative)
}

// CreateParent holds a descriptor for the final parent, creating missing
// directories beneath root without following symlinks. The caller closes it.
func CreateParent(root *os.File, relative string) (*os.File, string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, "", fmt.Errorf("unsafe artifact path %q", relative)
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, "", fmt.Errorf("unsafe artifact path %q", relative)
		}
	}
	if root == nil {
		return nil, "", fmt.Errorf("artifact root is nil")
	}
	rootFD, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, "", fmt.Errorf("duplicating trusted artifact root: %w", err)
	}
	current := os.NewFile(uintptr(rootFD), root.Name())
	if current == nil {
		_ = unix.Close(rootFD)
		return nil, "", fmt.Errorf("holding trusted artifact root")
	}
	for _, part := range parts[:len(parts)-1] {
		next, _, openErr := OpenChild(current, part, true)
		if openErr != nil && os.IsNotExist(openErr) {
			mkdirErr := unix.Mkdirat(int(current.Fd()), part, 0o755)
			if mkdirErr != nil && mkdirErr != unix.EEXIST {
				_ = current.Close()
				return nil, "", fmt.Errorf("creating artifact directory %s: %w", part, mkdirErr)
			}
			if mkdirErr == nil {
				if syncErr := current.Sync(); syncErr != nil {
					_ = current.Close()
					return nil, "", fmt.Errorf("persisting artifact directory %s: %w", part, syncErr)
				}
			}
			next, _, openErr = OpenChild(current, part, true)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, "", fmt.Errorf("opening artifact directory %s: %w", part, openErr)
		}
		_ = current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}

// CreateTemporarySibling opens a private, exclusive regular file beneath parent.
// The caller closes the file and removes its returned name on failure.
func CreateTemporarySibling(parent *os.File) (*os.File, string, error) {
	return CreateTemporarySiblingWithPrefix(parent, ".nvkr-download-")
}

// CreateTemporarySiblingWithPrefix gives private stages an explicit diagnostic label.
// Labels are single safe basenames; neither labels nor random names confer identity.
func CreateTemporarySiblingWithPrefix(parent *os.File, prefix string) (*os.File, string, error) {
	if prefix == "" || len(prefix) > 64 || prefix == "." || prefix == ".." || strings.IndexFunc(prefix, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c))
	}) >= 0 {
		return nil, "", fmt.Errorf("invalid private stage prefix")
	}
	if parent == nil {
		return nil, "", fmt.Errorf("artifact parent directory is nil")
	}
	for attempt := 0; attempt < 32; attempt++ {
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(token[:])
		fd, err := unix.Openat(int(parent.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == unix.EEXIST {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
			return nil, "", fmt.Errorf("opening private artifact download")
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("could not allocate a private artifact download name")
}

// OpenDirectory holds a directory without following a final-path symlink.
// The caller closes the returned handle.
func OpenDirectory(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening directory %s", path)
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path %s is not a real directory", path)
	}
	return f, info, nil
}

// OpenRegular holds one exact pathname inode without allowing a final
// symlink or a FIFO/device substitution to block the caller during admission.
func OpenRegular(path string) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("path %s is not a regular file", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("holding regular file %s", path)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("regular file %s changed while opening", path)
	}
	return file, info, nil
}

// OpenChild opens one regular file or directory beneath the held parent without
// following symlinks or blocking on a substituted FIFO. The caller closes it.
func OpenChild(parent *os.File, name string, directory bool) (*os.File, os.FileInfo, error) {
	if parent == nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, nil, fmt.Errorf("unsafe child path %q", name)
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening child %q", name)
	}
	info, err := f.Stat()
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("child %q has an unexpected file type", name)
	}
	return f, info, nil
}

// Entry describes an inspected child. File is held for regular files and
// directories and must be closed by the caller; symlinks carry only Target.
type Entry struct {
	Kind   string
	Mode   fs.FileMode
	Target string
	File   *os.File
	Info   os.FileInfo
}

// InspectChild opens a regular file/directory or reads a relative symlink without
// following it. Relative names the child's position in the containing tree and
// prevents symlinks from escaping that tree.
func InspectChild(parent *os.File, name, relative string) (Entry, error) {
	if parent == nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return Entry{}, fmt.Errorf("unsafe child path %q", name)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Entry{}, fmt.Errorf("inspecting source entry %s: %w", relative, err)
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		file, info, err := OpenChild(parent, name, true)
		if err != nil {
			return Entry{}, fmt.Errorf("opening source directory %s: %w", relative, err)
		}
		return Entry{Kind: "directory", Mode: info.Mode(), File: file, Info: info}, nil
	case unix.S_IFREG:
		file, info, err := OpenChild(parent, name, false)
		if err != nil {
			return Entry{}, fmt.Errorf("opening source file %s: %w", relative, err)
		}
		return Entry{Kind: "file", Mode: info.Mode(), File: file, Info: info}, nil
	case unix.S_IFLNK:
		target, err := readSourceSymlink(parent, name, stat)
		if err != nil {
			return Entry{}, fmt.Errorf("reading source symlink %s: %w", relative, err)
		}
		if !sourceSymlinkSafe(filepath.ToSlash(relative), target) {
			return Entry{}, fmt.Errorf("source symlink %s escapes the authenticated closure", relative)
		}
		return Entry{Kind: "symlink", Mode: fs.FileMode(stat.Mode & 0o777), Target: target}, nil
	default:
		return Entry{}, fmt.Errorf("unsupported source entry %s (mode %#o)", relative, stat.Mode)
	}
}

func readSourceSymlink(parent *os.File, name string, before unix.Stat_t) (string, error) {
	buffer := make([]byte, 256)
	for len(buffer) <= 1<<20 {
		n, err := unix.Readlinkat(int(parent.Fd()), name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			var after unix.Stat_t
			if err := unix.Fstatat(int(parent.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return "", err
			}
			if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Size != after.Size {
				return "", fmt.Errorf("source symlink changed while being inspected")
			}
			return string(buffer[:n]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
	return "", fmt.Errorf("source symlink target is too long")
}

func sourceSymlinkSafe(relative, target string) bool {
	if target == "" || filepath.IsAbs(target) || strings.ContainsRune(target, '\x00') {
		return false
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(relative)), target))
	return resolved != ".." && !strings.HasPrefix(resolved, ".."+string(filepath.Separator)) && !filepath.IsAbs(resolved)
}

// SameMetadata joins device/inode identity, mode, size and modification time.
// It is a mutation check, not content authentication.
func SameMetadata(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Mode() == after.Mode() && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime())
}

// WorkspaceRelative reports whether value names a nonempty workspace path.
// Callers still resolve filesystem access through held directories.
func WorkspaceRelative(value string) bool {
	clean := filepath.Clean(value)
	return strings.TrimSpace(value) != "" && !filepath.IsAbs(value) && clean != "." && clean != ".." &&
		!strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// ClearDirectory removes contents relative to an owned directory descriptor.
// It never follows symlinks or removes the descriptor's own entry. The caller
// owns the bounded recovery context and final unlink of its private stage.
func ClearDirectory(ctx context.Context, dir *os.File) error { return clearDirectory(ctx, dir, 0) }
func clearDirectory(ctx context.Context, dir *os.File, depth int) error {
	if ctx == nil || dir == nil {
		return fmt.Errorf("private stage cleanup requires context and descriptor")
	}
	if depth > 32 {
		return fmt.Errorf("private stage cleanup depth budget")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Rewind between deletion batches: directory cookies need not remain
		// stable after unlink, and the remaining contents are still bounded by ctx.
		if _, err := dir.Seek(0, 0); err != nil {
			return err
		}
		entries, err := dir.ReadDir(64)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			flags := 0
			if entry.IsDir() {
				child, _, err := OpenChild(dir, entry.Name(), true)
				if err != nil {
					return err
				}
				err = clearDirectory(ctx, child, depth+1)
				_ = child.Close()
				if err != nil {
					return err
				}
				flags = unix.AT_REMOVEDIR
			}
			if err := unix.Unlinkat(int(dir.Fd()), entry.Name(), flags); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
