// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
	// Hold each admitted stack for this preparation only. Shared outputs must
	// not repeatedly acquire the same pinned metadata during the three passes.
	type admittedStack struct {
		selection authored.Catalog
		stack     *catalog.Stack
	}
	var stacks []admittedStack
	loadStack := func(ctx context.Context, selection authored.Catalog) (*catalog.Stack, error) {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "local source catalog admission canceled")
		}
		for _, admitted := range stacks {
			if admitted.selection.Base == selection.Base && slices.Equal(admitted.selection.Overlays, selection.Overlays) {
				return admitted.stack, nil
			}
		}
		stack, err := loadFactoryCatalogStack(ctx, definitionPath, selection)
		if err != nil {
			return nil, err
		}
		selection.Overlays = slices.Clone(selection.Overlays)
		stacks = append(stacks, admittedStack{selection: selection, stack: stack})
		return stack, nil
	}
	if _, err := compileDocumentsWithLoader(ctx, definitionPath, definition, environmentPath, environment, "local-source-preparation", loadStack); err != nil {
		return NativeBuildPreparation{}, err
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
		stack, err := loadStack(ctx, selection)
		if err != nil {
			return NativeBuildPreparation{}, err
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
			if !selected.FromSource || strings.TrimSuffix(selected.Repo, ".git") != localNVKERepository {
				return original, false, nil
			}
			return selected, true, nil
		}, true, loadStack)
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	return NativeBuildPreparation{Definition: prepared.Definition, Environment: prepared.Environment}, nil
}
