// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
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
			if !retained[file.after] {
				_ = os.Remove(file.after)
			}
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
			if err := os.Remove(file.after); err != nil && !errors.Is(err, os.ErrNotExist) {
				retained[file.after], retained[file.before] = true, true
				failures = append(failures, fmt.Errorf("cannot withdraw %s; stages %s and %s retained: %w", file.change.path, file.after, file.before, err))
				continue
			}
			var err error
			if file.change.exists {
				keep, replaceErr := replaceFamilyFile(context.WithoutCancel(ctx), file.change.path, file.before, file.after, file.change.after, file.change.mode, os.Rename)
				err = replaceErr
				if keep {
					retained[file.after] = true
				}
			} else {
				// Withdraw into our private stage before checking, so a racing
				// editor's replacement is never unlinked without being retained.
				err = os.Rename(file.change.path, file.after)
				if err == nil {
					raw, _, readErr := readFamilyFile(context.WithoutCancel(ctx), file.after)
					if readErr != nil || !bytes.Equal(raw, file.change.after) {
						if restoreErr := os.Link(file.after, file.change.path); restoreErr != nil {
							retained[file.after] = true
							err = fmt.Errorf("new output changed; displaced bytes retained at %s: %w", file.after, restoreErr)
						} else {
							err = fmt.Errorf("new output changed; external bytes restored")
						}
					}
				}
			}
			if err == nil {
				err = syncOverlayDirectory(filepath.Dir(file.change.path))
			}
			if err != nil {
				if file.before != "" {
					retained[file.before] = true
				}
				failures = append(failures, fmt.Errorf("restore %s: %w", file.change.path, err))
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
		var err error
		if file.change.exists {
			keep, replaceErr := replaceFamilyFile(ctx, file.change.path, file.after, file.before, file.change.before, file.change.mode, rename)
			err = replaceErr
			if keep {
				retained[file.before] = true
			}
		} else {
			// Link publishes a new regular file without replacing a racing writer.
			err = os.Link(file.after, file.change.path)
		}
		if err != nil {
			code := e.CodeInternal
			if errors.Is(err, os.ErrExist) {
				code = e.CodeWriteConflict
			}
			var coded *e.Error
			if errors.As(err, &coded) {
				return rollback(err)
			}
			return rollback(e.Wrap(code, err, "committing family output %q", file.change.path))
		}
		written++
		if err := os.Remove(file.after); err != nil {
			return rollback(e.Wrap(e.CodeInternal, err, "removing published family stage"))
		}
		if err := syncOverlayDirectory(filepath.Dir(file.change.path)); err != nil {
			return rollback(e.Wrap(e.CodeInternal, err, "syncing family output"))
		}
	}
	expected := maps.Clone(snapshot.files)
	ignored := map[string]bool{}
	for _, file := range files {
		expected[file.change.path] = familyFileState{digest: sha256.Sum256(file.change.after), mode: file.change.mode}
		ignored[file.after] = true
		if file.change.exists {
			expected[file.before] = familyFileState{digest: sha256.Sum256(file.change.before), mode: file.change.mode}
		}
	}
	if err := snapshot.checkState(ctx, expected, ignored); err != nil {
		return rollback(err)
	}

	return nil
}

// replaceFamilyFile retains the actual displaced inode before inspecting it.
// Publication and restoration use no-replace links: a save between comparison
// and publication stays at its destination, with displaced bytes retained when
// restoration cannot safely put them back. There may be a brief absent path.
func replaceFamilyFile(ctx context.Context, target, candidate, displaced string, expected []byte, mode os.FileMode, rename func(string, string) error) (bool, error) {
	if err := rename(target, displaced); err != nil {
		return false, err
	}
	restore := func(cause error) (bool, error) {
		if err := os.Link(displaced, target); err != nil {
			return true, e.Wrap(e.CodeWriteConflict, errors.Join(cause, err), "family output %q changed; displaced bytes retained at %s", target, displaced)
		}
		return false, cause
	}
	raw, gotMode, err := readFamilyFile(ctx, displaced)
	if err != nil && ctx.Err() != nil {
		return restore(contextError(ctx, err))
	}
	if err != nil || !bytes.Equal(raw, expected) || gotMode != mode {
		return restore(e.New(e.CodeWriteConflict, "family output %q changed during replacement", target))
	}
	if err := os.Link(candidate, target); err != nil {
		return restore(err)
	}
	return false, nil
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
