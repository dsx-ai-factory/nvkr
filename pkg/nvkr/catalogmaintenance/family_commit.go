// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type familyChange struct {
	path          string
	before, after []byte
	exists        bool
	mode          os.FileMode
}

// commitFamilyChanges holds every cooperating owner's writer guard, checks the
// captured input bytes, and stages both directions before the first rename.
// Ordinary commit failures roll back only bytes still owned by this operation.
// This is not crash-atomic across files: interrupted commits remain detectable
// by the ordinary generated-baseline checks and must be repaired before use.
func commitFamilyChanges(ctx context.Context, snapshot *familySnapshot, changes []familyChange, guards []string) error {
	return commitFamilyChangesWithRename(ctx, snapshot, changes, guards, os.Rename)
}

func commitFamilyChangesWithRename(ctx context.Context, snapshot *familySnapshot, changes []familyChange, guards []string, rename func(string, string) error) error {
	guards = slices.Compact(slices.Sorted(slices.Values(guards)))
	var held []*os.File
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(held[i].Fd()), syscall.LOCK_UN)
			_ = held[i].Close()
		}
	}()
	for _, name := range guards {
		if err := ctx.Err(); err != nil {
			return contextError(ctx, err)
		}
		if !safeRelative(name) {
			return e.New(e.CodeUsage, "invalid family writer guard")
		}
		if parent := filepath.Dir(name); parent != "." {
			if err := admitPath(parent); err != nil {
				return err
			}
		}
		fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "opening family writer guard %q", name)
		}
		file := os.NewFile(uintptr(fd), name)
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return e.New(e.CodeWriteConflict, "family writer guard must be regular")
		}
		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = file.Close()
			return e.Wrap(e.CodeWriteConflict, err, "another owner is updating family files")
		}
		held = append(held, file)
	}
	if err := snapshot.checkOriginals(ctx); err != nil {
		return err
	}
	type staged struct {
		change        familyChange
		after, before string
	}
	var files []staged
	retained := map[string]bool{}
	defer func() {
		for _, file := range files {
			_ = os.Remove(file.after)
			if file.before != "" && !retained[file.before] {
				_ = os.Remove(file.before)
			}
		}
	}()
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return contextError(ctx, err)
		}
		if !safeRelative(change.path) {
			return e.New(e.CodeUsage, "unsafe family output path")
		}
		if err := admitPath(filepath.Dir(change.path)); err != nil {
			return err
		}
		item := staged{change: change}
		files = append(files, item)
		after, err := stageFamilyFile(ctx, change.path, change.after, change.mode, ".nvkr-family-after-")
		files[len(files)-1].after = after
		if err != nil {
			return err
		}
		if change.exists {
			before, err := stageFamilyFile(ctx, change.path, change.before, change.mode, ".nvkr-family-before-")
			files[len(files)-1].before = before
			if err != nil {
				return err
			}
		}
	}
	written := 0
	rollback := func(cause error) error {
		var failures []error
		// Cleanup deliberately ignores cancellation: every committed member must be
		// restored or explicitly reported as incomplete before releasing the guards.
		for i := written - 1; i >= 0; i-- {
			file := files[i]
			raw, _, err := readFamilyFile(context.WithoutCancel(ctx), file.change.path)
			if err != nil || !bytes.Equal(raw, file.change.after) {
				if file.change.exists {
					retained[file.before] = true
					failures = append(failures, fmt.Errorf("%s changed after family write; backup %s retained", file.change.path, file.before))
				} else {
					failures = append(failures, fmt.Errorf("new output %s changed after family write; external bytes retained", file.change.path))
				}
				continue
			}
			if file.change.exists {
				err = os.Rename(file.before, file.change.path)
			} else {
				err = os.Remove(file.change.path)
			}
			if err == nil {
				err = syncOverlayDirectory(filepath.Dir(file.change.path))
			}
			if err != nil {
				if file.before != "" {
					retained[file.before] = true
				}
				failures = append(failures, fmt.Errorf("restore %s from %s: %w", file.change.path, file.before, err))
			}
		}
		if len(failures) != 0 {
			return e.Wrap(e.CodeWriteConflict, errors.Join(append([]error{cause}, failures...)...), "family update incomplete; retain backups and repair the checkout before use")
		}
		return cause
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return rollback(contextError(ctx, err))
		}
		current, _, err := readFamilyFile(ctx, file.change.path)
		if file.change.exists {
			if err != nil || !bytes.Equal(current, file.change.before) {
				return rollback(e.New(e.CodeWriteConflict, "family output %q changed before commit", file.change.path))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(e.New(e.CodeWriteConflict, "new family output %q already exists", file.change.path))
		}
		if file.change.exists {
			err = rename(file.after, file.change.path)
		} else {
			// Link publishes a new regular file with no replacement of a racing writer.
			err = os.Link(file.after, file.change.path)
		}
		if err != nil {
			code := e.CodeInternal
			if errors.Is(err, os.ErrExist) {
				code = e.CodeWriteConflict
			}
			return rollback(e.Wrap(code, err, "committing family output %q", file.change.path))
		}
		written++
		if err := syncOverlayDirectory(filepath.Dir(file.change.path)); err != nil {
			return rollback(e.Wrap(e.CodeInternal, err, "syncing family output"))
		}
	}
	return nil
}

func stageFamilyFile(ctx context.Context, target string, raw []byte, mode os.FileMode, prefix string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(target), prefix)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "staging family output")
	}
	name := file.Name()
	defer func() { _ = file.Close() }()
	if err := ctx.Err(); err != nil {
		return name, contextError(ctx, err)
	}
	if err := file.Chmod(mode); err != nil {
		return name, e.Wrap(e.CodeInternal, err, "setting family output mode")
	}
	if _, err := file.Write(raw); err != nil {
		return name, e.Wrap(e.CodeInternal, err, "writing family output stage")
	}
	if err := file.Sync(); err != nil {
		return name, e.Wrap(e.CodeInternal, err, "syncing family output stage")
	}
	if err := file.Close(); err != nil {
		return name, e.Wrap(e.CodeInternal, err, "closing family output stage")
	}
	return name, nil
}

func (snapshot *familySnapshot) change(ctx context.Context, name string) (familyChange, bool, error) {
	candidate, _, err := readFamilyFile(ctx, filepath.Join(snapshot.root, name))
	if err != nil {
		return familyChange{}, false, err
	}
	state, exists := snapshot.files[name]
	mode := os.FileMode(0644)
	if exists {
		mode = state.mode
	}
	if exists && sha256.Sum256(candidate) == state.digest {
		return familyChange{}, false, nil
	}
	var before []byte
	if exists {
		before, _, err = readFamilyFile(ctx, name)
		if err != nil {
			return familyChange{}, false, err
		}
		if sha256.Sum256(before) != state.digest {
			return familyChange{}, false, e.New(e.CodeWriteConflict, "family input %q changed during preparation", name)
		}
	}
	return familyChange{path: name, before: before, after: candidate, exists: exists, mode: mode}, true, nil
}
