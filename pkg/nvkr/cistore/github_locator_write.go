// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"io"
	"os"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func writeGitHubLocatorSet(ctx context.Context, index string, directory *os.File, info os.FileInfo, selection GitHubLocatorSelection, locators []githubLocatorBytes) (result error) {
	// Recheck freshness using a new descriptor (with a fresh directory offset),
	// while all mutations remain relative to the original held directory.
	current, currentInfo, err := securepath.OpenDirectory(index)
	if err != nil {
		return e.New(e.CodeImmutableViolation, "reopening CI locator index failed")
	}
	entries, readErr := current.ReadDir(1)
	closeErr := current.Close()
	if !os.SameFile(info, currentInfo) || readErr != io.EOF || len(entries) != 0 || closeErr != nil {
		return e.New(e.CodeImmutableViolation, "CI locator index changed before publication")
	}
	flat := selection.Name != "" || selection.MergeMultiple
	committed := make([]string, 0, len(locators))
	defer func() {
		if result == nil {
			return
		}
		for _, name := range committed {
			if err := removeGitHubLocator(directory, name, flat); err != nil {
				result = e.New(e.CodeImmutableViolation, "rollback of CI locator index failed")
			}
		}
		if err := directory.Sync(); err != nil {
			result = e.New(e.CodeImmutableViolation, "persisting CI locator rollback failed")
		}
	}()
	for _, locator := range locators {
		if ctx.Err() != nil {
			return safeIOError(ctx, e.CodeTimeout, "CI locator publication canceled")
		}
		if err := writeGitHubLocator(directory, locator, flat); err != nil {
			return err
		}
		committed = append(committed, locator.name)
	}
	if err := directory.Sync(); err != nil {
		return e.New(e.CodeImmutableViolation, "persisting CI locator index failed")
	}
	if ctx.Err() != nil {
		return safeIOError(ctx, e.CodeTimeout, "CI locator publication canceled")
	}
	if err := requireGitHubLocatorDirectory(index, info); err != nil {
		return e.New(e.CodeImmutableViolation, "CI locator index changed during publication")
	}
	return nil
}

func writeGitHubLocator(root *os.File, locator githubLocatorBytes, flat bool) (result error) {
	parent := root
	var childInfo os.FileInfo
	var temporary string
	createdDirectory, published := false, false
	defer func() {
		if !flat && parent != nil && parent != root {
			if err := parent.Close(); err != nil {
				result = e.New(e.CodeImmutableViolation, "closing CI locator artifact directory failed")
			}
		}
		if result != nil && (createdDirectory || temporary != "" || published) {
			if err := rollbackGitHubLocatorWrite(root, locator.name, flat, childInfo, temporary, published); err != nil {
				result = err
			}
		}
	}()
	if !flat {
		if err := unix.Mkdirat(int(root.Fd()), locator.name, 0o700); err != nil {
			return e.New(e.CodeImmutableViolation, "creating exclusive CI locator artifact directory failed")
		}
		createdDirectory = true
		var err error
		parent, childInfo, err = securepath.OpenChild(root, locator.name, true)
		if err != nil {
			return e.New(e.CodeImmutableViolation, "holding CI locator artifact directory failed")
		}
	}
	file, temporaryName, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.New(e.CodeImmutableViolation, "creating private CI locator file failed")
	}
	temporary = temporaryName
	n, writeErr := file.Write(locator.raw)
	if writeErr == nil && n == len(locator.raw) {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || n != len(locator.raw) || closeErr != nil {
		return e.New(e.CodeImmutableViolation, "writing private CI locator file failed")
	}
	name := locator.name + locatorSuffix
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), name); err != nil {
		return e.New(e.CodeImmutableViolation, "publishing CI locator without replacement failed")
	}
	temporary, published = "", true
	if err := parent.Sync(); err != nil {
		return e.New(e.CodeImmutableViolation, "persisting CI locator file failed")
	}
	return nil
}

// The caller still owns a failed individual write, including failures discovered
// while closing its child directory. Reopen through the held root after close;
// remove a final name only when this operation's no-replace rename succeeded.
func rollbackGitHubLocatorWrite(root *os.File, name string, flat bool, childInfo os.FileInfo, temporary string, published bool) error {
	parent := root
	if !flat && childInfo != nil {
		var currentInfo os.FileInfo
		var err error
		parent, currentInfo, err = securepath.OpenChild(root, name, true)
		if err != nil {
			return e.New(e.CodeImmutableViolation, "reopening failed CI locator artifact directory failed")
		}
		if !os.SameFile(childInfo, currentInfo) {
			_ = parent.Close()
			return e.New(e.CodeImmutableViolation, "failed CI locator artifact directory changed before rollback")
		}
	}
	failed := false
	ownedNames := []string{temporary}
	if published {
		ownedNames = append(ownedNames, name+locatorSuffix)
	}
	for _, owned := range ownedNames {
		if owned != "" {
			if err := unix.Unlinkat(int(parent.Fd()), owned, 0); err != nil && err != unix.ENOENT {
				failed = true
			}
		}
	}
	if err := parent.Sync(); err != nil {
		failed = true
	}
	if parent != root {
		if err := parent.Close(); err != nil {
			failed = true
		}
	}
	if !flat {
		if err := unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
			failed = true
		}
		if err := root.Sync(); err != nil {
			failed = true
		}
	}
	if failed {
		return e.New(e.CodeImmutableViolation, "rollback of failed CI locator publication failed")
	}
	return nil
}

func removeGitHubLocator(root *os.File, name string, flat bool) error {
	if flat {
		return unix.Unlinkat(int(root.Fd()), name+locatorSuffix, 0)
	}
	parent, _, err := securepath.OpenChild(root, name, true)
	if err != nil {
		return err
	}
	removeErr := unix.Unlinkat(int(parent.Fd()), name+locatorSuffix, 0)
	closeErr := parent.Close()
	directoryErr := unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
	if removeErr != nil {
		return removeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return directoryErr
}
