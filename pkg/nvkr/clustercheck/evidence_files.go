// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package clustercheck retains collected cluster evidence as private,
// byte-exact diagnostic files. The lane is descriptive (ADR-29): nothing
// written here is an authenticated receipt, a lifecycle object or a passing
// verdict, and no manifest, report interpretation or observation structure is
// produced.
package clustercheck

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

// incompletePrefix marks staged or unsynced components.
const incompletePrefix = ".incomplete-"

const (
	// maxComponentNameBytes bounds one member name while leaving room for the
	// reserved in-flight prefix inside the 255-byte filesystem element limit.
	maxComponentNameBytes = 200
	evidenceCopyChunk     = 128 << 10
)

// EvidenceFiles writes exact, bounded payloads into a new private directory.
// The writer is not safe for concurrent use.
type EvidenceFiles struct {
	root            string
	maxPayloadBytes int64
	directory       *os.Root
}

// OpenEvidenceFiles creates a private directory at a new, absolute root.
// Missing parents are created with mode 0700 and retained if opening fails.
func OpenEvidenceFiles(ctx context.Context, root string, maxPayloadBytes int64) (*EvidenceFiles, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "opening evidence files requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening evidence files")
	}
	if !safetext.Valid(root, 4096, false) || strings.TrimSpace(root) != root || filepath.Clean(root) != root ||
		!filepath.IsAbs(root) {
		return nil, e.New(e.CodeUsage, "evidence directory requires a bounded safe absolute path")
	}
	if maxPayloadBytes <= 0 {
		return nil, e.New(e.CodeUsage, "evidence payloads require a positive byte bound")
	}
	parent, err := openEvidenceParent(ctx, filepath.Dir(root))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, e.Wrap(e.CodeTimeout, ctxErr, "opening evidence parent directory")
		}
		return nil, e.Wrap(e.CodeImmutableViolation, err, "opening evidence parent directory")
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(root)
	if err := parent.Mkdir(name, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, e.New(e.CodeWriteConflict, "evidence directory already exists")
		}
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating evidence directory")
	}
	created, err := parent.Lstat(name)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "inspecting evidence directory")
	}
	if !created.IsDir() {
		return nil, e.New(e.CodeImmutableViolation, "created evidence path is no longer a directory")
	}
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errors.Join(e.Wrap(e.CodeImmutableViolation, err, "holding evidence directory"),
			removeCreatedDirectory(parent, name, created))
	}
	held, err := directory.Stat(".")
	if err != nil || !os.SameFile(created, held) || held.Mode().Perm() != 0o700 {
		_ = directory.Close()
		if err == nil {
			err = errors.New("evidence directory must be the new private 0700 directory")
		}
		return nil, errors.Join(e.Wrap(e.CodeImmutableViolation, err, "inspecting evidence directory"),
			removeCreatedDirectory(parent, name, created))
	}
	if err := syncEvidenceDirectory(parent); err != nil {
		_ = directory.Close()
		return nil, errors.Join(e.Wrap(e.CodeImmutableViolation, err, "syncing evidence parent directory"),
			removeCreatedDirectory(parent, name, created))
	}
	return &EvidenceFiles{root: root, maxPayloadBytes: maxPayloadBytes, directory: directory}, nil
}

// openEvidenceParent walks real directories from the filesystem root. Existing
// aliases must be resolved by the caller before selecting an output path.
func openEvidenceParent(ctx context.Context, absolute string) (*os.Root, error) {
	base := filepath.VolumeName(absolute) + string(filepath.Separator)
	current, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(absolute, base), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, err := openEvidenceDirectory(ctx, current, part)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func openEvidenceDirectory(ctx context.Context, parent *os.Root, name string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening evidence directory")
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := syncEvidenceDirectory(parent); err != nil {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("evidence parent is not a real directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	held, err := child.Stat(".")
	if err != nil || !os.SameFile(info, held) {
		_ = child.Close()
		return nil, errors.New("evidence parent changed while opening")
	}
	return child, nil
}

func syncEvidenceDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// Directory returns the private directory path this writer created.
func (files *EvidenceFiles) Directory() string {
	if files == nil {
		return ""
	}
	return files.root
}

// WriteComponent stages one bounded payload under the reserved in-flight
// prefix, then publishes it at the exact member name without replacing any
// existing entry or symlink. The payload bytes are preserved exactly: no
// normalization, digesting or interpretation happens here. The caller owns
// cancellation of blocking reads. A retained incomplete marker prevents
// retrying that component in the same directory.
func (files *EvidenceFiles) WriteComponent(ctx context.Context, name string, payload io.Reader) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "writing evidence requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing evidence component")
	}
	if files == nil || files.directory == nil {
		return e.New(e.CodeUsage, "writing evidence requires an open writer")
	}
	if !validComponentName(name) {
		return e.New(e.CodeUsage, "evidence component name %q is not a safe member name", name)
	}
	if payload == nil {
		return e.New(e.CodeUsage, "evidence component %s requires a payload", name)
	}
	if err := files.checkDirectory(); err != nil {
		return err
	}
	stagedName := incompletePrefix + name
	file, err := files.directory.OpenFile(stagedName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return e.New(e.CodeWriteConflict, "evidence component %s already has an in-flight write", name)
		}
		return e.Wrap(e.CodeImmutableViolation, err, "staging evidence component %s", name)
	}
	// Publish only after checking that the staged path still names the synced bytes.
	if err := copyEvidencePayload(ctx, file, payload, files.maxPayloadBytes); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return e.Wrap(e.CodeImmutableViolation, err, "syncing staged evidence component %s", name)
	}
	staged, err := file.Stat()
	closeErr := file.Close()
	if err != nil || closeErr != nil || !staged.Mode().IsRegular() {
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = errors.New("staged evidence component is not a regular file")
		}
		return e.Wrap(e.CodeImmutableViolation, err, "completing staged evidence component %s", name)
	}
	pathInfo, err := files.directory.Lstat(stagedName)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(staged, pathInfo) {
		if err == nil {
			err = errors.New("staged evidence component changed while writing")
		}
		return e.Wrap(e.CodeImmutableViolation, err, "evidence component %s stage changed", name)
	}
	if err := files.checkDirectory(); err != nil {
		return err
	}
	directory, err := files.directory.Open(".")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening evidence directory for sync")
	}
	defer func() { _ = directory.Close() }()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "publishing evidence component")
	}
	return files.publishComponent(directory, stagedName, name)
}

func (files *EvidenceFiles) publishComponent(directory *os.File, stagedName, name string) error {
	if err := files.directory.Link(stagedName, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.Join(e.New(e.CodeWriteConflict, "evidence component %s already exists", name),
				files.directory.Remove(stagedName))
		}
		return e.Wrap(e.CodeImmutableViolation, err, "publishing evidence component %s", name)
	}
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "evidence component %s is visible but its publication was not synced", name)
	}
	if err := files.directory.Remove(stagedName); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "evidence component %s was synced but its incomplete marker could not be removed", name)
	}
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing evidence component %s marker removal", name)
	}
	return nil
}

func (files *EvidenceFiles) checkDirectory() error {
	held, err := files.directory.Stat(".")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "inspecting held evidence directory")
	}
	current, err := os.Lstat(files.root)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "checking evidence directory path")
	}
	if !os.SameFile(held, current) || held.Mode().Perm() != 0o700 {
		return e.New(e.CodeImmutableViolation, "evidence directory changed before publication")
	}
	return nil
}

// Close releases the held directory anchor. Completed components and any
// distinguishable in-flight leftovers stay on disk for inspection.
func (files *EvidenceFiles) Close() error {
	if files == nil || files.directory == nil {
		return nil
	}
	err := files.directory.Close()
	files.directory = nil
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "closing evidence directory")
	}
	return nil
}

func validComponentName(name string) bool {
	if name == "" || len(name) > maxComponentNameBytes || !safetext.Valid(name, maxComponentNameBytes, false) {
		return false
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

func copyEvidencePayload(ctx context.Context, destination io.Writer, source io.Reader, maxBytes int64) error {
	buffer := make([]byte, evidenceCopyChunk)
	var total int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "writing evidence payload")
		}
		read, readErr := source.Read(buffer)
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "writing evidence payload")
		}
		if read > 0 {
			emptyReads = 0
			if int64(read) > maxBytes-total {
				return e.New(e.CodeUsage, "evidence payload exceeds its %d-byte bound", maxBytes)
			}
			total += int64(read)
			if _, err := destination.Write(buffer[:read]); err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "writing evidence payload")
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return e.Wrap(e.CodeUnavailable, readErr, "reading evidence payload")
		}
		if read == 0 {
			emptyReads++
			if emptyReads == 100 {
				return e.Wrap(e.CodeUnavailable, io.ErrNoProgress, "reading evidence payload")
			}
		}
	}
}

// removeCreatedDirectory only removes the same empty directory this call created.
func removeCreatedDirectory(parent *os.Root, name string, expected os.FileInfo) error {
	current, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "inspecting refused evidence directory")
	}
	if !os.SameFile(expected, current) {
		return e.New(e.CodeImmutableViolation, "refusing to remove replaced evidence directory")
	}
	if err := parent.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return e.Wrap(e.CodeImmutableViolation, err, "removing refused evidence directory")
	}
	return nil
}
