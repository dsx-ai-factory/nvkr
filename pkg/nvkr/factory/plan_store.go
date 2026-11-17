// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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

// Named addresses are deterministic for one object's logical role and exact
// bytes. Full integrity remains bound by every owning manifest, not this suffix.
func namedPlanStorePath(logical string, raw []byte, extension string) (string, error) {
	name := PlanStoreDirectory + "/" + logical + "@" + strings.TrimPrefix(pipeline.FileDigest(raw), "sha256:")[:12] + extension
	if !isPlanStorePath(name) {
		return "", e.New(e.CodePipelineDrift, "unsafe logical Plan object name %q", logical)
	}
	return name, nil
}

func isPlanStorePath(name string) bool {
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) || !safeGeneratedPath(name) || !strings.HasPrefix(name, PlanStoreDirectory+"/") || (path.Ext(name) != ".json" && path.Ext(name) != ".sh") {
		return false
	}
	base := strings.TrimSuffix(path.Base(name), path.Ext(name))
	if path.Dir(name) == PlanStoreDirectory {
		// Previous generated manifests retain retirement authority during migration.
		return canonical.IsDigest("sha256:" + base)
	}
	kind, _, _ := strings.Cut(strings.TrimPrefix(name, PlanStoreDirectory+"/"), "/")
	switch kind {
	case "plan", "catalogs", "components", "sources", "steps", "resolved-components", "scripts", "graph", "definition", "composition", "environment", "actions", "overlays", "tools", "outputs", "routes", "runtime-inputs", "targets", "records":
	default:
		return false
	}
	at := strings.LastIndex(base, "@")
	if at <= 0 || len(base)-at-1 != 12 {
		return false
	}
	suffix := base[at+1:]
	for _, c := range suffix {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func planStoreMatches(name string, raw []byte) bool {
	if !isPlanStorePath(name) {
		return false
	}
	if path.Dir(name) == PlanStoreDirectory {
		return planStorePath(raw, path.Ext(name)) == name
	}
	base := strings.TrimSuffix(path.Base(name), path.Ext(name))
	suffix := base[strings.LastIndex(base, "@")+1:]
	return suffix == strings.TrimPrefix(pipeline.FileDigest(raw), "sha256:")[:12]
}

func isPlanStoreRecord(name string) bool { return isPlanStorePath(name) && path.Ext(name) == ".json" }
func isPlanStoreScript(name string) bool { return isPlanStorePath(name) && path.Ext(name) == ".sh" }

// Other manifests describe shared-file ownership, not executable authority.
// Inspect only the finite producer namespace, never arbitrary repository paths.
func planStoreOwners(ctx context.Context, root, except string) (map[string]planStoreOwner, error) {
	result := map[string]planStoreOwner{}
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
		if !ok || layout.manifestPath == except {
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
			prior := result[member.Path]
			if prior.Member.Path != "" && prior.Member.SHA256 != member.SHA256 {
				return nil, e.New(e.CodePipelineDrift, "shared Plan owners disagree on full digest for %s", member.Path)
			}
			prior.Member = member
			prior.Bundles = append(prior.Bundles, entry.Name())
			result[member.Path] = prior
			if len(result) > maxGeneratedDirEntries {
				return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds member bound")
			}
		}
	}
	return result, nil
}

// Store-wide admission belongs to generation and drift checks, never Plan loads.
// Checks report garbage without mutating it. A writer holds the store lock from
// this inventory through manifest publication and the subsequent sweep.
func checkPlanStore(ctx context.Context, root string, bundle *GeneratedBundle) error {
	_, err := inspectPlanStore(ctx, root, bundle, false)
	return err
}

func inspectPlanStore(ctx context.Context, root string, bundle *GeneratedBundle, writing bool) ([]GeneratedMember, error) {
	if bundle.Engine != EngineGitHub {
		return nil, nil
	}
	owners, err := planStoreOwners(ctx, root, bundle.ManifestPath)
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for name, owner := range owners {
		allowed[name] = owner.Member.SHA256
	}
	for name, raw := range bundle.Files {
		if isPlanStorePath(name) {
			digest := pipeline.FileDigest(raw)
			if prior := allowed[name]; prior != "" && prior != digest {
				return nil, e.New(e.CodePipelineDrift, "short Plan address collision at %s", name)
			}
			allowed[name] = digest
			owner := owners[name]
			owner.Member = GeneratedMember{Path: name, SHA256: digest, Mode: 0o644}
			owner.Bundles = append(owner.Bundles, path.Base(path.Dir(bundle.ManifestPath)))
			owners[name] = owner
		}
	}
	if len(allowed) > maxGeneratedDirEntries {
		return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds member bound")
	}
	entries, directories, err := planStoreInventory(ctx, root)
	if err != nil {
		return nil, err
	}
	plannedEntries := map[string]bool{path.Base(PlanStoreIndexPath): true}
	addEntry := func(name string) {
		for current := strings.TrimPrefix(name, PlanStoreDirectory+"/"); current != "."; current = path.Dir(current) {
			plannedEntries[current] = true
		}
	}
	for name := range allowed {
		addEntry(name)
	}
	for _, name := range entries {
		addEntry(PlanStoreDirectory + "/" + name)
	}
	for _, name := range directories {
		addEntry(PlanStoreDirectory + "/" + name)
	}
	if len(plannedEntries) > maxGeneratedDirEntries {
		return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds directory bound")
	}
	seen := map[string]bool{}
	var garbage []GeneratedMember
	total := int64(0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "reading shared Plan inventory")
		}
		name := PlanStoreDirectory + "/" + entry
		if name == PlanStoreIndexPath {
			info, err := os.Lstat(filepath.Join(root, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 || info.Size() > maxGeneratedFileBytes {
				return nil, e.New(e.CodePipelineDrift, "shared Plan index is unsafe or exceeds member bound")
			}
			continue
		}
		staging := strings.HasPrefix(path.Base(name), ".nvkr-run-stage-")
		if !isPlanStorePath(name) && !staging {
			return nil, e.New(e.CodePipelineDrift, "unexpected shared Plan store member %q", name)
		}
		filename := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm() != 0o644 && !(staging && info.Mode().Perm() == 0o600)) {
			return nil, e.New(e.CodePipelineDrift, "shared Plan member %q is unsafe or has changed mode", name)
		}
		if info.Size() > maxPlanTreeBytes-total {
			return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds size bound")
		}
		raw, err := readGeneratedContext(ctx, filename)
		if err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading shared Plan member")
		}
		total += int64(len(raw))
		if total > maxPlanTreeBytes || (!staging && !planStoreMatches(name, raw)) {
			return nil, e.New(e.CodePipelineDrift, "shared Plan member %q differs from its content address", name)
		}
		seen[name] = true
		if expected := allowed[name]; expected != "" && pipeline.FileDigest(raw) != expected {
			return nil, e.New(e.CodePipelineDrift, "shared Plan member %q differs from its owning manifest", name)
		}
		if allowed[name] == "" {
			garbage = append(garbage, GeneratedMember{Path: name, SHA256: pipeline.FileDigest(raw), Mode: uint32(info.Mode().Perm())})
		}
	}
	for _, name := range sortedKeys(allowed) {
		if seen[name] {
			continue
		}
		raw, incoming := bundle.Files[name]
		if !writing || !incoming {
			bundles := owners[name].Bundles
			slices.Sort(bundles)
			return nil, e.New(e.CodePipelineDrift, "shared Plan member %q is missing; regenerate owning bundles %q", name, bundles)
		}
		total += int64(len(raw))
		if total > maxPlanTreeBytes {
			return nil, e.New(e.CodePipelineDrift, "shared Plan store exceeds size bound")
		}
	}
	if !writing && len(garbage) != 0 {
		names := make([]string, 0, len(garbage))
		for _, member := range garbage {
			names = append(names, member.Path)
		}
		return nil, e.New(e.CodePipelineDrift, "unreferenced shared Plan members %q; run generate with the same Definition and Environment without --check to sweep them", names)
	}

	if !writing {
		if err := checkPlanStoreIndex(ctx, root); err != nil {
			return nil, err
		}
		files, directories := map[string]bool{path.Base(PlanStoreIndexPath): true}, map[string]bool{".": true}
		for name := range allowed {
			rel := strings.TrimPrefix(name, PlanStoreDirectory+"/")
			files[rel] = true
			for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
				directories[parent] = true
			}
		}
		if err := checkGeneratedInventory(ctx, filepath.Join(root, PlanStoreDirectory), files, directories); err != nil {
			return nil, fmt.Errorf("%w; if this is unexpected empty-directory drift, run generate with the same Definition and Environment without --check to prune empty directories", err)
		}
	}
	return garbage, nil
}
