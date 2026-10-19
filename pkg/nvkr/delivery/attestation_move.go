// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func preparedAttestationPaths(ctx context.Context, source, out string) (string, string, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(out) == "" {
		return "", "", e.New(e.CodeUsage, "prepared and final OCI layout paths are required")
	}
	// Resolve parent aliases, including the platform's temporary-directory alias,
	// while retaining the final source component for no-follow admission.
	parent, err := filepath.EvalSymlinks(filepath.Dir(source))
	if err != nil {
		return "", "", e.Wrap(e.CodeVerifyFailed, err, "resolving prepared layout parent")
	}
	source, err = filepath.Abs(filepath.Join(parent, filepath.Base(source)))
	if err != nil {
		return "", "", err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Lstat(out); err == nil {
		return "", "", e.New(e.CodeImmutableViolation, "refusing to overwrite OCI layout %s", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	// The output parent need not exist yet. Resolve its nearest existing ancestor
	// before any creation so an alias cannot hide a destination within source.
	parent = filepath.Dir(out)
	tail := filepath.Base(out)
	for {
		if err := deliveryContext(ctx); err != nil {
			return "", "", err
		}
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if err := preparedLayoutSameFilesystem(ctx, source, resolved); err != nil {
				return "", "", err
			}
			out = filepath.Join(resolved, tail)
			break
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", "", e.Wrap(e.CodeVerifyFailed, err, "resolving final layout parent")
		}
		tail = filepath.Join(filepath.Base(parent), tail)
		parent = filepath.Dir(parent)
	}
	if source == out || strings.HasPrefix(out, source+string(filepath.Separator)) ||
		strings.HasPrefix(source, out+string(filepath.Separator)) {
		return "", "", e.New(e.CodeUsage, "prepared and final OCI layouts must not overlap")
	}
	return source, out, nil
}

// Only this exact flat OCI tree can be transferred. In particular, unreferenced
// files outside blobs, symlinks and hard-linked files are not disposable input.
func preparedLayoutInventory(ctx context.Context, root string) (map[string]os.FileInfo, error) {
	inventory := map[string]os.FileInfo{}
	var walk func(string, string) error
	walk = func(path, relative string) error {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		directory, info, err := securepath.OpenDirectory(path)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening prepared layout directory")
		}
		defer directory.Close()
		inventory[relative] = info
		maximum := maxDeliveryObjects * 8
		switch relative {
		case ".":
			maximum = 3
		case "blobs":
			maximum = 1
		}
		entries, err := directory.ReadDir(maximum + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return e.Wrap(e.CodeVerifyFailed, err, "reading prepared layout inventory")
		}
		if len(entries) > maximum {
			return e.New(e.CodeVerifyFailed, "prepared layout inventory exceeds its entry bound")
		}
		for _, entry := range entries {
			if err := deliveryContext(ctx); err != nil {
				return err
			}
			name := filepath.ToSlash(filepath.Join(relative, entry.Name()))
			child := filepath.Join(path, entry.Name())
			if name == "blobs" || name == "blobs/sha256" {
				if err := walk(child, name); err != nil {
					return err
				}
				continue
			}
			blob := strings.HasPrefix(name, "blobs/sha256/") && isDigest("sha256:"+strings.TrimPrefix(name, "blobs/sha256/"))
			if !entry.Type().IsRegular() || (name != "index.json" && name != "oci-layout" && !blob) {
				return e.New(e.CodeVerifyFailed, "prepared layout contains unexpected entry %q", name)
			}
			file, info, err := securepath.OpenChild(directory, entry.Name(), false)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "opening prepared layout entry")
			}
			var stat unix.Stat_t
			statErr := unix.Fstat(int(file.Fd()), &stat)
			if err := errors.Join(statErr, file.Close()); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "inspecting prepared layout entry")
			}
			if stat.Nlink != 1 {
				return e.New(e.CodeVerifyFailed, "prepared layout entry %q is not an exclusively owned regular file", name)
			}
			inventory[name] = info
		}
		return nil
	}
	err := walk(root, ".")
	return inventory, err
}

func comparePreparedInventory(ctx context.Context, root string, expected map[string]os.FileInfo) error {
	actual, err := preparedLayoutInventory(ctx, root)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return e.New(e.CodeVerifyFailed, "prepared layout inventory changed during attestation")
	}
	for name, before := range expected {
		if !securepath.SameMetadata(before, actual[name]) {
			return e.New(e.CodeVerifyFailed, "prepared layout entry %q changed during attestation", name)
		}
	}
	return nil
}

func preparedLayoutSameFilesystem(ctx context.Context, source, stage string) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	var original, destination unix.Stat_t
	if err := unix.Lstat(source, &original); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "inspecting prepared layout filesystem")
	}
	if err := unix.Lstat(stage, &destination); err != nil {
		return e.Wrap(e.CodeInternal, err, "inspecting attestation staging filesystem")
	}
	if original.Dev != destination.Dev {
		return e.New(e.CodeEngineCapability, "prepared and final layouts require the same filesystem for ownership transfer")
	}
	return nil
}

func consumePreparedBlobs(ctx context.Context, source, stage string, inventory map[string]os.FileInfo, unchanged map[string]bool) error {
	if err := comparePreparedInventory(ctx, source, inventory); err != nil {
		return err
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(source))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening prepared layout parent")
	}
	defer parent.Close()
	destination, _, err := securepath.OpenDirectory(stage)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening attestation stage")
	}
	defer destination.Close()
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	// This is the ownership-transfer point. Everything beneath the private stage
	// is now disposable on failure; no partially rewritten source is restored.
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), filepath.Base(source), int(destination.Fd()), ".prepared"); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "claiming prepared layout")
	}
	claimed := filepath.Join(stage, ".prepared")
	if err := comparePreparedInventory(ctx, claimed, inventory); err != nil {
		return err
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting prepared layout transfer")
	}
	digests := make([]string, 0, len(unchanged))
	for digest := range unchanged {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		name := strings.TrimPrefix(digest, "sha256:")
		target := filepath.Join(stage, "blobs", "sha256", name)
		if _, err := os.Lstat(target); err == nil {
			// A shared small config may already belong to new evidence. Complete
			// inspection below authenticates its exact bytes and full closure.
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := transportarchive.PublishAtNoReplace(unix.AT_FDCWD, filepath.Join(claimed, "blobs", "sha256", name), unix.AT_FDCWD, target); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "transferring unchanged publication blob")
		}
	}
	if err := os.RemoveAll(claimed); err != nil {
		return e.Wrap(e.CodeInternal, err, "discarding obsolete preparation metadata")
	}
	return nil
}

func publishAttestedLayout(ctx context.Context, stage, out string) error {
	return publishAttestedLayoutWithSync(ctx, stage, out, (*os.File).Sync)
}

func publishAttestedLayoutWithSync(ctx context.Context, stage, out string, syncParent func(*os.File) error) error {
	inventory, err := preparedLayoutInventory(ctx, stage)
	if err != nil {
		return err
	}
	// Persist the transferred files and newly written metadata before exposing the
	// complete layout. This is a file sync, not another payload read or copy.
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		var file *os.File
		var err error
		path := filepath.Join(stage, filepath.FromSlash(name))
		if inventory[name].IsDir() {
			file, _, err = securepath.OpenDirectory(path)
		} else {
			file, _, err = securepath.OpenRegular(path)
		}
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return e.Wrap(e.CodeInternal, err, "persisting attested layout")
		}
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening final layout parent")
	}
	defer parent.Close()
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), filepath.Base(stage), int(parent.Fd()), filepath.Base(out)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return e.Wrap(e.CodeImmutableViolation, err, "refusing to overwrite final layout")
		}
		return e.Wrap(e.CodeInternal, err, "committing attested layout")
	}
	if err := syncParent(parent); err != nil {
		rollback := transportarchive.PublishAtNoReplace(int(parent.Fd()), filepath.Base(out), int(parent.Fd()), filepath.Base(stage))
		return e.Wrap(e.CodeInternal, errors.Join(err, rollback, parent.Sync()), "persisting attested layout commit; rollback attempted")
	}
	return nil
}
