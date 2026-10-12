// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package catalog

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

func readCatalogRegularBeneathContext(ctx context.Context, rootPath, relative string) ([]byte, error) {
	file, before, err := openCatalogPath(rootPath, relative, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if before.Size() < 0 || before.Size() > MaxCatalogFileBytes {
		return nil, fmt.Errorf("catalog file %q is not within the %d-byte bound", relative, MaxCatalogFileBytes)
	}
	raw, err := readCatalogFile(ctx, file, MaxCatalogFileBytes)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || int64(len(raw)) != after.Size() {
		return nil, fmt.Errorf("catalog file %q changed while being read", relative)
	}
	if err := ValidateFilePayload(relative, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func readCatalogDirectoryBeneathContext(ctx context.Context, rootPath, relative string) ([]string, error) {
	directory, _, err := openCatalogPath(rootPath, relative, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	state := &catalogWalkState{}
	if err := walkCatalogDirectory(ctx, directory, "", 0, state); err != nil {
		return nil, err
	}
	return state.files, nil
}

func walkCatalogDirectory(ctx context.Context, directory *os.File, prefix string, depth int, state *catalogWalkState) error {
	entries, err := readCatalogDirectoryEntries(ctx, directory, state)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := requireCatalogContext(ctx, "catalog directory read"); err != nil {
			return err
		}
		name := entry.Name()
		relative := path.Join(prefix, name)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("catalog entry %q is a symlink", relative)
		}
		if entry.IsDir() {
			if depth >= MaxCatalogTraversalDepth {
				return fmt.Errorf("catalog directory %q exceeds the depth-%d bound", relative, MaxCatalogTraversalDepth)
			}
			child, _, err := openCatalogChild(directory, name, true)
			if err != nil {
				return err
			}
			walkErr := walkCatalogDirectory(ctx, child, relative, depth+1, state)
			closeErr := child.Close()
			if walkErr != nil {
				return walkErr
			}
			if closeErr != nil {
				return closeErr
			}
			continue
		}
		file, _, err := openCatalogChild(directory, name, false)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		state.files = append(state.files, relative)
	}
	return nil
}

func openCatalogPath(rootPath, relative string, directory bool) (*os.File, os.FileInfo, error) {
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	current := os.NewFile(uintptr(rootFD), rootPath)
	if current == nil {
		_ = unix.Close(rootFD)
		return nil, nil, fmt.Errorf("opening catalog root %q", rootPath)
	}
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		wantDirectory := index < len(parts)-1 || directory
		next, info, openErr := openCatalogChild(current, part, wantDirectory)
		closeErr := current.Close()
		if openErr != nil {
			return nil, nil, openErr
		}
		if closeErr != nil {
			_ = next.Close()
			return nil, nil, closeErr
		}
		current = next
		if index == len(parts)-1 {
			return current, info, nil
		}
	}
	_ = current.Close()
	return nil, nil, fmt.Errorf("empty catalog-relative path")
}

func openCatalogChild(parent *os.File, name string, directory bool) (*os.File, os.FileInfo, error) {
	if parent == nil || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, nil, fmt.Errorf("unsafe catalog path component %q", name)
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening catalog child %q", name)
	}
	info, err := file.Stat()
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("catalog child %q has an unsupported file type", name)
	}
	return file, info, nil
}
