// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// PlanStoreIndexPath is the generated, descriptive inventory of store variants
// and their owners. Loads authenticate their own manifests without consulting it.
const PlanStoreIndexPath = PlanStoreDirectory + "/index.json"

type planStoreOwner struct {
	Member  GeneratedMember
	Bundles []string
}

type planStoreVariant struct {
	File    string   `json:"file"`
	SHA256  string   `json:"sha256"`
	Bundles []string `json:"bundles"`
}

func planStoreIndex(ctx context.Context, root string, incoming ...*GeneratedBundle) ([]byte, error) {
	except := make([]string, 0, len(incoming))
	for _, bundle := range incoming {
		except = append(except, bundle.ManifestPath)
	}
	owners, err := planStoreOwners(ctx, root, except...)
	if err != nil {
		return nil, err
	}
	for _, bundle := range incoming {
		for name, raw := range bundle.Files {
			if !isPlanStorePath(name) {
				continue
			}
			owner := owners[name]
			owner.Member = GeneratedMember{Path: name, SHA256: pipeline.FileDigest(raw), Mode: 0o644}
			owner.Bundles = append(owner.Bundles, path.Base(path.Dir(bundle.ManifestPath)))
			owners[name] = owner
		}
	}
	jobs, err := planStoreNavigation(ctx, root, owners, incoming)
	if err != nil {
		return nil, err
	}
	return encodePlanStoreIndex(owners, jobs)
}

func encodePlanStoreIndex(owners map[string]planStoreOwner, jobs map[string][]planStoreJobVariant) ([]byte, error) {
	groups := map[string][]planStoreVariant{}
	for _, name := range sortedKeys(owners) {
		owner := owners[name]
		slices.Sort(owner.Bundles)
		relative := strings.TrimPrefix(name, PlanStoreDirectory+"/")
		logical := strings.TrimSuffix(relative, path.Ext(relative))
		if index := strings.LastIndex(logical, "@"); index >= 0 {
			logical = logical[:index]
		}
		groups[logical] = append(groups[logical], planStoreVariant{relative, owner.Member.SHA256, owner.Bundles})
	}
	raw, err := treeJSON(planStoreIndexDocument{Objects: groups, Jobs: jobs})
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxGeneratedFileBytes {
		return nil, e.New(e.CodePipelineDrift, "shared Plan index exceeds member bound")
	}
	return raw, nil
}

func checkPlanStoreIndex(ctx context.Context, root string, bundle *GeneratedBundle, owners map[string]planStoreOwner) error {
	filename := filepath.Join(root, PlanStoreIndexPath)
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		return e.New(e.CodePipelineDrift, "shared Plan index is missing or unsafe; regenerate the bundle")
	}
	raw, err := readGeneratedContext(ctx, filename)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading shared Plan index")
	}
	var index planStoreIndexDocument
	if err := decodeTreeJSON(raw, &index); err != nil {
		return err
	}
	expected, err := encodePlanStoreIndex(owners, index.Jobs)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return e.New(e.CodePipelineDrift, "shared Plan index differs from bundle ownership; regenerate the bundle")
	}
	return checkPlanStoreNavigation(ctx, bundle, owners, index.Jobs)
}

// Publication follows the bundle manifests and sweep. An interruption leaves a
// stale descriptive index; the next generation recomputes it from current owners.
func writePlanStoreIndex(ctx context.Context, root string, raw []byte) error {
	if err := ensureGeneratedDirectory(root, PlanStoreDirectory); err != nil {
		return err
	}
	filename := filepath.Join(root, PlanStoreIndexPath)
	staged, err := os.CreateTemp(filepath.Dir(filename), ".nvkr-run-stage-*")
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "staging shared Plan index")
	}
	defer func() { _ = staged.Close(); _ = os.Remove(staged.Name()) }()
	if err := staged.Chmod(0o644); err != nil {
		return err
	}
	if _, err := copyContext(ctx, staged, bytes.NewReader(raw)); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), filename)
}

// Traverse only the reserved store, without following links. Both existing
// files and directories count toward the finite inventory bound.
func planStoreInventory(ctx context.Context, root string) ([]string, []string, error) {
	directory := filepath.Join(root, PlanStoreDirectory)
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	var files, directories []string
	count := 0
	var walk func(string) error
	walk = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "reading shared Plan inventory")
		}
		name := path.Join(PlanStoreDirectory, relative)
		if err := validateGeneratedDirectoryChain(root, name); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "unsafe shared Plan directory")
		}
		dir, _, err := securepath.OpenDirectory(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "opening shared Plan directory")
		}
		entries, readErr := dir.ReadDir(maxGeneratedDirEntries - count + 1)
		closeErr := dir.Close()
		count += len(entries)
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || count > maxGeneratedDirEntries {
			return e.New(e.CodePipelineDrift, "shared Plan store exceeds directory bound or cannot be read")
		}
		for _, entry := range entries {
			child := path.Join(relative, entry.Name())
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), ".nvkr-run-stage-") || !isPlanStorePath(PlanStoreDirectory+"/"+child+"/object@000000000000.json") {
					return e.New(e.CodePipelineDrift, "unexpected shared Plan directory %q", child)
				}
				directories = append(directories, child)
				if err := walk(child); err != nil {
					return err
				}
			} else {
				files = append(files, child)
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, nil, err
	}
	slices.Sort(files)
	return files, directories, nil
}

// Empty generated directories can remain after a crash during object retirement.
// Remove only empty directories in the already admitted store; never recursively.
func prunePlanStoreDirectories(ctx context.Context, root string) error {
	var directories []string
	count := 0
	err := filepath.WalkDir(filepath.Join(root, PlanStoreDirectory), func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "pruning shared Plan directories")
		}
		count++
		if count > maxGeneratedDirEntries+1 {
			return e.New(e.CodePipelineDrift, "shared Plan store exceeds directory bound")
		}
		if entry.IsDir() && name != filepath.Join(root, PlanStoreDirectory) {
			directories = append(directories, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slices.SortFunc(directories, func(a, b string) int { return len(b) - len(a) })
	for _, name := range directories {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "pruning shared Plan directories")
		}
		entries, err := os.ReadDir(name)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(name); err != nil {
				return err
			}
		}
	}
	return nil
}
