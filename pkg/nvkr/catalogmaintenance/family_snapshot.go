// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const familySnapshotBytes = 512 << 20
const familySnapshotFileBytes = 64 << 20
const familySnapshotFiles = 16384

type familyFileState struct {
	digest [sha256.Size]byte
	mode   fs.FileMode
}
type familySnapshot struct {
	root        string
	roots       []string
	directories map[string]bool
	files       map[string]familyFileState
	guards      map[string]bool
}

// snapshotFamilyInputs copies bounded authored/generated input roots, never Git
// metadata or a working checkout. The private staging directory has no writers
// except this preparation. Original files are checked again before commitment.
func snapshotFamilyInputs(ctx context.Context, config Config, configPath string, guardPaths ...string) (*familySnapshot, error) {
	roots := map[string]bool{".github": true}
	add := func(name string) {
		if name != "" {
			head, _, _ := strings.Cut(name, "/")
			roots[head] = true
		}
	}
	for _, name := range []string{configPath, config.Definition, config.Environment, config.ProducerOverlays, config.QualificationPolicy, config.DevelopmentQualificationPolicy} {
		add(name)
	}
	for _, run := range otherRuns(config) {
		add(run.Definition)
		add(run.Environment)
		add(run.QualificationPolicy)
	}
	for _, member := range config.Family.Catalogs {
		add(member.Catalog)
	}
	names := make([]string, 0, len(roots))
	for root := range roots {
		if root == ".git" || !safeRelative(root) {
			return nil, e.New(e.CodeUsage, "family inputs cannot select repository metadata")
		}
		names = append(names, root)
	}
	slices.Sort(names)
	root, err := os.MkdirTemp("", "nvkr-family-inputs-")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating family input staging")
	}
	snapshot := &familySnapshot{root: root, roots: names, directories: map[string]bool{}, files: map[string]familyFileState{}}
	snapshot.guards = map[string]bool{}
	for _, name := range guardPaths {
		snapshot.guards[filepath.ToSlash(name)] = true
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(root)
		}
	}()
	var total int64
	for _, name := range names {
		err = filepath.WalkDir(name, func(filename string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return contextError(ctx, err)
			}
			if walkErr != nil {
				return e.Wrap(e.CodeInvalidCatalog, walkErr, "capturing family input %q", filename)
			}
			relative := filepath.ToSlash(filename)
			if !safeFamilyPath(relative) || strings.Count(relative, "/") > 32 || entry.Type()&os.ModeSymlink != 0 {
				return e.New(e.CodeInvalidCatalog, "unsafe family input %q", relative)
			}
			destination := filepath.Join(root, filename)
			if entry.IsDir() {
				if len(snapshot.directories)+len(snapshot.files) >= familySnapshotFiles {
					return e.New(e.CodeInvalidCatalog, "excessive family input entries")
				}
				snapshot.directories[relative] = true
				if err := os.MkdirAll(destination, 0700); err != nil {
					return e.Wrap(e.CodeInternal, err, "staging family input directory")
				}
				return nil
			}
			// Persistent writer guards are process coordination, not authored bytes.
			if snapshot.guards[relative] || strings.HasSuffix(relative, "/.pins.lock.yaml.lock") {
				return nil
			}
			if !entry.Type().IsRegular() || len(snapshot.directories)+len(snapshot.files) >= familySnapshotFiles {
				return e.New(e.CodeInvalidCatalog, "unsupported or excessive family input files")
			}
			raw, mode, err := readFamilyFile(ctx, filename)
			if err != nil {
				return err
			}
			total += int64(len(raw))
			if total > familySnapshotBytes {
				return e.New(e.CodeInvalidCatalog, "family input snapshot exceeds byte limit")
			}
			if err := os.WriteFile(destination, raw, mode); err != nil {
				return e.Wrap(e.CodeInternal, err, "staging family input %q", relative)
			}
			if err := os.Chmod(destination, mode); err != nil {
				return e.Wrap(e.CodeInternal, err, "preserving captured family input mode")
			}
			snapshot.files[relative] = familyFileState{digest: sha256.Sum256(raw), mode: mode}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	success = true
	return snapshot, nil
}

func readFamilyFile(ctx context.Context, filename string) ([]byte, fs.FileMode, error) {
	held, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "catalog family input", MaxBytes: familySnapshotFileBytes})
	if err != nil {
		return nil, 0, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading family input %q", filename))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, familySnapshotFileBytes)
	if err != nil {
		return nil, 0, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading family input %q", filename))
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, e.New(e.CodeInvalidCatalog, "family input %q is not a regular file", filename)
	}
	return raw, info.Mode().Perm(), nil
}

func (snapshot *familySnapshot) checkOriginals(ctx context.Context) error {
	return snapshot.checkState(ctx, snapshot.files, nil)
}

func (snapshot *familySnapshot) checkState(ctx context.Context, expectedFiles map[string]familyFileState, ignored map[string]bool) error {
	seenDirectories := map[string]bool{}
	for _, root := range snapshot.roots {
		if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return contextError(ctx, err)
			}
			if walkErr != nil {
				return e.Wrap(e.CodeWriteConflict, walkErr, "family input listing changed")
			}
			name = filepath.ToSlash(name)
			if entry.IsDir() {
				if !snapshot.directories[name] {
					return e.New(e.CodeWriteConflict, "family input directory %q was added", name)
				}
				seenDirectories[name] = true
				return nil
			}
			if snapshot.guards[name] || strings.HasSuffix(name, "/.pins.lock.yaml.lock") {
				return nil
			}
			if ignored[name] {
				return nil
			}
			if _, known := expectedFiles[name]; !known {
				return e.New(e.CodeWriteConflict, "family input %q was added", name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if len(seenDirectories) != len(snapshot.directories) {
		return e.New(e.CodeWriteConflict, "family input directory membership changed")
	}
	names := make([]string, 0, len(expectedFiles))
	for name := range expectedFiles {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		raw, mode, err := readFamilyFile(ctx, name)
		if err != nil {
			return err
		}
		expected := expectedFiles[name]
		if sha256.Sum256(raw) != expected.digest || mode != expected.mode {
			return e.New(e.CodeWriteConflict, "family input %q changed during preparation", name)
		}
	}
	return nil
}

// This is path admission for copying, not generated ownership. The factory
// authenticates store names, digests and manifests before any family mutation.
func safeFamilyPath(name string) bool {
	return safeRelative(name) || (strings.HasPrefix(name, factory.PlanStoreDirectory+"/") && safeRelative(strings.ReplaceAll(name, "@", "-")))
}

// Family pin changes retain the directory layout. Ordinary authored generation
// owns creation and pruning; refusing here prevents a successful but dirty store.
func (snapshot *familySnapshot) checkPreparedDirectories(ctx context.Context, paths map[string]bool) error {
	for name := range snapshot.directories {
		if err := ctx.Err(); err != nil {
			return contextError(ctx, err)
		}
		info, err := os.Lstat(filepath.Join(snapshot.root, name))
		if err != nil || !info.IsDir() {
			return e.New(e.CodeEngineCapability, "family bump changes directory %q; regenerate through the authored bundle owners", name)
		}
	}
	for name := range paths {
		if !snapshot.directories[filepath.ToSlash(filepath.Dir(name))] {
			return e.New(e.CodeEngineCapability, "family bump creates a directory for %q; regenerate through the authored bundle owners", name)
		}
	}
	return nil
}
