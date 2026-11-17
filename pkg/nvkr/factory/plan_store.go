// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// PlanStoreDirectory contains immutable frozen Plan records and script blobs shared by
// generated GitHub bundles. Addresses describe exact bytes, never Plan identity.
const PlanStoreDirectory = ".github/nvkr-plan-store"

func planStorePath(raw []byte, extension string) string {
	return PlanStoreDirectory + "/" + strings.TrimPrefix(pipeline.FileDigest(raw), "sha256:") + extension
}

func isPlanStorePath(name string) bool {
	if path.Dir(name) != PlanStoreDirectory || (path.Ext(name) != ".json" && path.Ext(name) != ".sh") {
		return false
	}
	return canonical.IsDigest("sha256:" + strings.TrimSuffix(path.Base(name), path.Ext(name)))
}

func isPlanStoreRecord(name string) bool { return isPlanStorePath(name) && path.Ext(name) == ".json" }
func isPlanStoreScript(name string) bool { return isPlanStorePath(name) && path.Ext(name) == ".sh" }

// Other manifests describe shared-file ownership, not executable authority.
// Inspect only the finite producer namespace, never arbitrary repository paths.
func otherPlanStoreMembers(ctx context.Context, root string, bundle *GeneratedBundle) (map[string]bool, error) {
	result := map[string]bool{}
	if err := validateGeneratedDirectoryChain(root, ".github"); err != nil {
		return nil, err
	}
	directory, _, err := securepath.OpenDirectory(filepath.Join(root, ".github"))
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "opening shared Plan owners")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxGeneratedDirEntries + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > maxGeneratedDirEntries {
		return nil, e.New(e.CodePipelineDrift, "shared Plan owners exceed directory bound")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "reading shared Plan owners")
		}
		layout, ok := githubWorkflowNamespace(".github/workflows/" + entry.Name() + ".yml")
		if !ok || layout.manifestPath == bundle.ManifestPath {
			continue
		}
		owner := &GeneratedBundle{Engine: EngineGitHub, WorkflowPath: layout.workflowPath, ManifestPath: layout.manifestPath, PlanPath: layout.planPath}
		members, err := priorGeneratedMembers(ctx, root, owner)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if !isPlanStorePath(member.Path) {
				continue
			}
			if member.SHA256 != "sha256:"+strings.TrimSuffix(path.Base(member.Path), path.Ext(member.Path)) {
				return nil, e.New(e.CodePipelineDrift, "shared Plan manifest address differs from its bytes")
			}
			result[member.Path] = true
			if len(result) > maxGeneratedDirEntries {
				return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds member bound")
			}
		}
	}
	return result, nil
}

// The union of producer manifests owns the store. Extra objects, unsafe names,
// linked files and altered bytes fail closed even when another bundle owns them.
// During generation only incoming members may be absent before publication.
func checkPlanStore(ctx context.Context, root string, bundle *GeneratedBundle, writing bool) error {
	if bundle.Engine != EngineGitHub {
		return nil
	}
	allowed, err := otherPlanStoreMembers(ctx, root, bundle)
	if err != nil {
		return err
	}
	for name := range bundle.Files {
		if isPlanStorePath(name) {
			allowed[name] = true
		}
	}
	if len(allowed) > maxGeneratedDirEntries {
		return e.New(e.CodePipelineDrift, "shared Plan store exceeds member bound")
	}
	directory := filepath.Join(root, PlanStoreDirectory)
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) && len(allowed) == 0 {
		return nil
	}
	if writing {
		if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
			total := int64(0)
			for name := range allowed {
				raw, incoming := bundle.Files[name]
				if !incoming {
					return e.New(e.CodePipelineDrift, "shared Plan store is missing")
				}
				total += int64(len(raw))
				if total > maxPlanTreeBytes {
					return e.New(e.CodePipelineDrift, "shared Plan store exceeds size bound")
				}
			}
			return nil
		}
	}
	if err := validateGeneratedDirectoryChain(root, PlanStoreDirectory); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "unsafe shared Plan store")
	}
	basenames := map[string]bool{}
	for name := range allowed {
		basenames[path.Base(name)] = true
	}
	if err := checkGeneratedInventory(ctx, directory, basenames, map[string]bool{".": true}); err != nil {
		return err
	}
	total := int64(0)
	for _, name := range sortedKeys(allowed) {
		filename := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(filename)
		if writing && errors.Is(err, os.ErrNotExist) {
			if raw, incoming := bundle.Files[name]; incoming {
				total += int64(len(raw))
				if total > maxPlanTreeBytes {
					return e.New(e.CodePipelineDrift, "shared Plan store exceeds size bound")
				}
				continue
			}
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			return e.New(e.CodePipelineDrift, "shared Plan member %q is missing, unsafe or has changed mode", name)
		}
		if info.Size() > maxPlanTreeBytes-total {
			return e.New(e.CodePipelineDrift, "shared Plan store exceeds size bound")
		}
		raw, err := readGeneratedContext(ctx, filename)
		if err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading shared Plan member")
		}
		total += int64(len(raw))
		if total > maxPlanTreeBytes || planStorePath(raw, path.Ext(name)) != name {
			return e.New(e.CodePipelineDrift, "shared Plan member %q differs from its content address", name)
		}
	}
	return nil
}
