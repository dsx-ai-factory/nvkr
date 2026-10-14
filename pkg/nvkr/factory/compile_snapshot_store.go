// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Compilation is synchronous; this state has one owner and freezes each source
// read the first time it is used, including shared base catalogs and patches.
type inputState struct {
	source          catalog.ContextDataProvider
	files           map[string][]byte
	directories     map[string][]string
	usedFiles       map[string]bool
	usedDirectories map[string]bool
	totalBytes      int64
	entries         int
}

func newInputState() *inputState {
	return &inputState{files: map[string][]byte{}, directories: map[string][]string{},
		usedFiles: map[string]bool{}, usedDirectories: map[string]bool{}}
}

func (state *inputState) readFile(ctx context.Context, name string) ([]byte, error) {
	if err := snapshotContext(ctx); err != nil {
		return nil, err
	}
	if err := validateInputPath(name); err != nil {
		return nil, err
	}
	raw, found := state.files[name]
	if !found {
		if state.source == nil {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot is missing file %q", name)
		}
		if state.entries >= catalog.MaxCatalogDirectoryEntries {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot exceeds its entry bound")
		}
		var err error
		raw, err = state.source.ReadFileContext(ctx, name)
		if err != nil {
			return nil, snapshotReadError(ctx, err, name)
		}
		if int64(len(raw)) > catalog.MaxCatalogTotalBytes-state.totalBytes {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot exceeds its aggregate byte bound")
		}
		state.totalBytes += int64(len(raw))
		state.entries++
		state.files[name] = append([]byte(nil), raw...)
	}
	state.usedFiles[name] = true
	return append([]byte(nil), raw...), nil
}

func (state *inputState) readDirectory(ctx context.Context, name string) ([]string, error) {
	if err := snapshotContext(ctx); err != nil {
		return nil, err
	}
	if err := validateInputPath(name); err != nil {
		return nil, err
	}
	entries, found := state.directories[name]
	if !found {
		if state.source == nil {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot is missing directory fact %q", name)
		}
		if state.entries >= catalog.MaxCatalogDirectoryEntries {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot exceeds its entry bound")
		}
		var err error
		entries, err = state.source.ReadDirContext(ctx, name)
		if err != nil {
			return nil, snapshotReadError(ctx, err, name)
		}
		if len(entries)+1 > catalog.MaxCatalogDirectoryEntries-state.entries {
			return nil, e.New(e.CodeInvalidCatalog, "input snapshot exceeds its entry bound")
		}
		state.entries += len(entries) + 1
		state.directories[name] = append([]string{}, entries...)
	}
	state.usedDirectories[name] = true
	return append([]string(nil), entries...), nil
}

func snapshotReadError(ctx context.Context, err error, name string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "capturing input %q", name)
	}
	return e.Wrap(e.CodeInvalidCatalog, err, "capturing input %q", name)
}

func (state *inputState) snapshot(definitionPath, environmentPath string) InputSnapshot {
	result := InputSnapshot{SchemaVersion: InputSnapshotSchema, DefinitionPath: definitionPath,
		EnvironmentPath: environmentPath, Files: []InputFile{}, Directories: []InputDirectory{}}
	for _, name := range sortedKeys(state.files) {
		result.Files = append(result.Files, InputFile{Path: name, Data: append([]byte(nil), state.files[name]...)})
	}
	for _, name := range sortedKeys(state.directories) {
		result.Directories = append(result.Directories, InputDirectory{Path: name, Entries: append([]string{}, state.directories[name]...)})
	}
	return result
}

func validateInputPath(name string) error {
	if len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return e.New(e.CodeInvalidCatalog, "unsafe input snapshot path %q", name)
	}
	return catalog.ValidateFilePayload(name, nil)
}

func validateInputSnapshot(snapshot InputSnapshot) error {
	if snapshot.SchemaVersion != InputSnapshotSchema {
		return e.New(e.CodeInvalidCatalog, "unsupported input snapshot schema %q", snapshot.SchemaVersion)
	}
	for _, name := range []string{snapshot.DefinitionPath, snapshot.EnvironmentPath} {
		if err := validateInputPath(name); err != nil {
			return err
		}
	}
	entries := len(snapshot.Files) + len(snapshot.Directories)
	if entries > catalog.MaxCatalogDirectoryEntries {
		return e.New(e.CodeInvalidCatalog, "input snapshot exceeds its entry bound")
	}
	files := map[string]bool{}
	var total int64
	previous := ""
	for _, file := range snapshot.Files {
		if err := validateInputPath(file.Path); err != nil {
			return err
		}
		if file.Path <= previous || int64(len(file.Data)) > catalog.MaxCatalogFileBytes ||
			int64(len(file.Data)) > catalog.MaxCatalogTotalBytes-total {
			return e.New(e.CodeInvalidCatalog, "input snapshot files must be sorted, unique and within byte bounds")
		}
		previous = file.Path
		total += int64(len(file.Data))
		files[file.Path] = true
	}
	previous = ""
	for _, directory := range snapshot.Directories {
		if err := validateInputPath(directory.Path); err != nil {
			return err
		}
		if directory.Path <= previous || files[directory.Path] || len(directory.Entries) > catalog.MaxCatalogDirectoryEntries-entries {
			return e.New(e.CodeInvalidCatalog, "input snapshot directories must be sorted, unique and bounded")
		}
		previous = directory.Path
		entries += len(directory.Entries)
		if err := validateInputDirectory(directory, files); err != nil {
			return err
		}
	}
	if !files[snapshot.DefinitionPath] || !files[snapshot.EnvironmentPath] {
		return e.New(e.CodeInvalidCatalog, "input snapshot omits Definition or Environment")
	}
	for _, file := range snapshot.Files {
		if err := validateInputAncestors(file.Path, files); err != nil {
			return err
		}
	}
	for _, directory := range snapshot.Directories {
		if err := validateInputAncestors(directory.Path, files); err != nil {
			return err
		}
	}
	return nil
}

func validateInputAncestors(name string, files map[string]bool) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if files[parent] {
			return e.New(e.CodeInvalidCatalog, "input snapshot path %q traverses file %q", name, parent)
		}
	}
	return nil
}

func validateInputDirectory(directory InputDirectory, files map[string]bool) error {
	previous := ""
	for _, entry := range directory.Entries {
		if err := validateInputPath(entry); err != nil {
			return err
		}
		if entry <= previous {
			return e.New(e.CodeInvalidCatalog, "input snapshot directory entries must be sorted and unique")
		}
		previous = entry
		if err := validateInputPath(path.Join(directory.Path, entry)); err != nil {
			return err
		}
	}
	for name := range files {
		if relative, inside := strings.CutPrefix(name, directory.Path+"/"); inside && !slices.Contains(directory.Entries, relative) {
			return e.New(e.CodeInvalidCatalog, "input snapshot directory %q omits captured file %q", directory.Path, name)
		}
	}
	return nil
}
