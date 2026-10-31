// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const localNVKERepository = "https://github.com/dsx-ai-factory/nvke"

// PrepareLocalNVKESource freezes an exact main revision into fresh unsigned local
// inputs. The ordinary Run captures and replays these predecessor-pinned overlays;
// neither authored inputs nor the NVKE checkout are changed.
func PrepareLocalNVKESource(ctx context.Context, repository, definitionPath, environmentPath, out, commit string) (NativeBuildPreparation, error) {
	if ctx == nil || !runtimeSourceCommitValid(commit) {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "local NVKE source preparation requires a context and full commit")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	root, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return NativeBuildPreparation{}, e.Wrap(e.CodeUsage, err, "resolve local source repository")
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil || cwd != root {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "local source preparation must run from its build repository")
	}
	// Local source selection has one maintained consumer, not an arbitrary graph
	// rewrite or a publication/qualification path.
	if definitionPath != "inputs/definitions/demo/amd64-nvidia-local-kvm.json" || environmentPath != "inputs/environments/local-amd64-kvm.json" {
		return NativeBuildPreparation{}, e.New(e.CodeEngineCapability, "local NVKE source preparation requires the default AMD64 NVIDIA inputs")
	}
	if filepath.IsAbs(out) || filepath.Clean(out) != out || !strings.HasPrefix(out, ".nvkr/") || strings.ContainsAny(out, "\r\n\x00") {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "local source preparation requires a fresh repository-relative .nvkr directory")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "local source preparation output already exists or cannot be inspected")
	}
	definition, err := authored.LoadDefinition(ctx, definitionPath)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	environment, err := authored.LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	if environment.Publication != nil || environment.Signing != nil {
		return NativeBuildPreparation{}, e.New(e.CodeEngineCapability, "local source preparation requires unsigned inputs")
	}
	if _, err := CompileDocuments(ctx, definitionPath, definition, environmentPath, environment, "local-source-preparation"); err != nil {
		return NativeBuildPreparation{}, err
	}
	// Both latest-main compatibility producers are already authored for both
	// native platforms. Reuse their complete validated components while retaining
	// the AMD64 disk producer and all other independent material selections.
	const nightlyDefinitionPath = "inputs/definitions/demo/arm64-nvidia-kvm.json"
	nightly, err := authored.LoadDefinition(ctx, nightlyDefinitionPath)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	compatible := make(map[string]catalog.Component)
	for _, output := range nightly.Outputs {
		for _, name := range output.Components {
			if name != "nvke-image-inputs" && name != "native-capi-exec-init-image" {
				continue
			}
			if _, exists := compatible[name]; exists {
				return NativeBuildPreparation{}, e.New(e.CodeInvalidCatalog, "current-main compatibility producer %q is ambiguous", name)
			}
			selection := nightly.Catalog
			if output.Catalog != nil {
				selection = *output.Catalog
			}
			stack, err := loadFactoryCatalogStack(ctx, nightlyDefinitionPath, selection)
			if err != nil {
				return NativeBuildPreparation{}, err
			}
			selected, ok := stack.Catalog.Components[name]
			if !ok || (name == "nvke-image-inputs" && (!selected.FromSource || strings.TrimSuffix(selected.Repo, ".git") != localNVKERepository)) || (name == "native-capi-exec-init-image" && (selected.FromSource || selected.Prebuilt == nil || selected.Prebuilt.Image == "")) {
				return NativeBuildPreparation{}, e.New(e.CodeInvalidCatalog, "current-main compatibility producer %q has an unexpected source", name)
			}
			compatible[name] = selected
		}
	}
	if len(compatible) != 2 {
		return NativeBuildPreparation{}, e.New(e.CodeInvalidCatalog, "current-main compatibility producers are missing")
	}
	revision, err := catalog.ResolveSourceRevisionAtRef(ctx, localNVKERepository, "refs/heads/main", commit)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	components := make(map[string]map[string]catalog.Component)
	for _, output := range definition.Outputs {
		selection := definition.Catalog
		if output.Catalog != nil {
			selection = *output.Catalog
		}
		stack, err := loadFactoryCatalogStack(ctx, definitionPath, selection)
		if err != nil {
			return NativeBuildPreparation{}, err
		}
		overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "local-current-main-compatibility", RequiresCatalogDigest: stack.Digest, Components: make(map[string]catalog.ComponentReplacement)}
		for name, component := range compatible {
			if original, exists := stack.Catalog.Components[name]; exists {
				if name == "nvke-image-inputs" {
					// Reuse current-main material commands without undoing the
					// selected catalog's native toolchain substitutions.
					component.ToolchainImage = original.ToolchainImage
					component.ToolchainImages = maps.Clone(original.ToolchainImages)
				}
				overlay.Components[name] = catalog.ComponentReplacement{Component: &component}
			}
		}
		if len(overlay.Components) > 0 {
			stack, err = catalog.ApplyStack(stack.Catalog, overlay)
			if err != nil {
				return NativeBuildPreparation{}, err
			}
		}
		frozen, err := catalog.ApplySourceRevision(stack, revision)
		if err != nil {
			return NativeBuildPreparation{}, err
		}
		components[output.Name] = frozen.Catalog.Components
	}
	prepared, err := prepareLocalInputsWithScope(ctx, definitionPath, environmentPath, out, map[string]any{}, func(*authored.Environment, string) error { return nil },
		func(output authored.DefinitionOutput, name string, original catalog.Component) (catalog.Component, bool, error) {
			selected, ok := components[output.Name][name]
			if !ok {
				return original, false, e.New(e.CodeInvalidCatalog, "source preparation lost component %q", name)
			}
			if name != "native-capi-exec-init-image" && (!selected.FromSource || strings.TrimSuffix(selected.Repo, ".git") != localNVKERepository) {
				return original, false, nil
			}
			return selected, true, nil
		}, true)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	return NativeBuildPreparation{Definition: prepared.Definition, Environment: prepared.Environment}, nil
}
