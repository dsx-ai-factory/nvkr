// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type preparedLocalInputs struct{ Definition, Environment string }
type localProducerTransform func(authored.DefinitionOutput, string, catalog.Component) (catalog.Component, bool, error)

// prepareLocalInputs owns the common output-scoped overlay, relocation, whole-graph
// validation and file emission for native dependency preparations. It changes no
// authored catalog, source pin, producer material or public lifecycle object.
func prepareLocalInputs(ctx context.Context, definitionPath, environmentPath, out string, files map[string]any,
	configure func(*authored.Environment, string) error, transform localProducerTransform,
) (preparedLocalInputs, error) {
	if filepath.IsAbs(out) || filepath.Clean(out) != out || !strings.HasPrefix(out, ".nvkr/") || strings.ContainsAny(out, "\r\n\x00") {
		return preparedLocalInputs{}, e.New(e.CodeUsage, "local input preparation requires a fresh repository-relative .nvkr directory")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return preparedLocalInputs{}, e.New(e.CodeUsage, "local input preparation directory already exists or cannot be inspected")
	}
	definition, err := authored.LoadDefinition(ctx, definitionPath)
	if err != nil {
		return preparedLocalInputs{}, err
	}
	environment, err := authored.LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return preparedLocalInputs{}, err
	}
	if environment.Publication != nil || environment.Signing != nil {
		return preparedLocalInputs{}, e.New(e.CodeEngineCapability, "native preparation is available only for local unqualified builds without publication or signing")
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return preparedLocalInputs{}, e.Wrap(e.CodeUsage, err, "resolving local input preparation directory")
	}
	if err := configure(&environment, absolute); err != nil {
		return preparedLocalInputs{}, err
	}
	prepared := preparedLocalInputs{Definition: filepath.Join(out, "definition.json"), Environment: filepath.Join(out, "environment.json")}
	stacks := make(map[string]*catalog.Stack)
	rebase := func(selection authored.Catalog) (authored.Catalog, error) {
		// Keep the same physical targets using locators the input-capture owner admits.
		locations := append([]string{selection.Base}, selection.Overlays...)
		for i, location := range locations {
			if !filepath.IsAbs(location) {
				location = filepath.Join(filepath.Dir(definitionPath), location)
			}
			location, err := filepath.Abs(location)
			if err != nil {
				return authored.Catalog{}, e.Wrap(e.CodeInvalidCatalog, err, "resolving local catalog reference")
			}
			relative, err := filepath.Rel(absolute, location)
			if err != nil {
				return authored.Catalog{}, e.Wrap(e.CodeInvalidCatalog, err, "relocating local catalog reference")
			}
			locations[i] = filepath.ToSlash(relative)
			if _, err := resolveInputPath(prepared.Definition, locations[i], i == 0); err != nil {
				return authored.Catalog{}, err
			}
		}
		selection.Base, selection.Overlays = locations[0], locations[1:]
		return selection, nil
	}
	baseSelection := definition.Catalog
	definition.Catalog, err = rebase(baseSelection)
	if err != nil {
		return preparedLocalInputs{}, err
	}
	changed := 0
	for i := range definition.Outputs {
		output := &definition.Outputs[i]
		selection := baseSelection
		if output.Catalog != nil {
			selection = *output.Catalog
		}
		rebased, err := rebase(selection)
		if err != nil {
			return preparedLocalInputs{}, err
		}
		stack, err := loadFactoryCatalogStack(ctx, definitionPath, selection)
		if err != nil {
			return preparedLocalInputs{}, err
		}
		selection = rebased
		overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "local-native-" + output.Name, RequiresCatalogDigest: stack.Digest, Components: make(map[string]catalog.ComponentReplacement)}
		for _, name := range output.Components {
			component, ok := stack.Catalog.Components[name]
			if !ok {
				return preparedLocalInputs{}, e.New(e.CodeInvalidCatalog, "missing selected component %q", name)
			}
			component, replace, err := transform(*output, name, component)
			if err != nil {
				return preparedLocalInputs{}, err
			}
			if replace {
				overlay.Components[name] = catalog.ComponentReplacement{Component: &component}
			}
		}
		if len(overlay.Components) > 0 {
			stack, err = catalog.ApplyStack(stack.Catalog, overlay)
			if err != nil {
				return preparedLocalInputs{}, err
			}
			file := "catalog-" + output.Name + ".json"
			files[file] = overlay
			selection.Overlays = append(selection.Overlays, file)
			changed++
		}
		output.Catalog = &selection
		stacks[output.Name] = stack
	}
	if changed == 0 {
		return preparedLocalInputs{}, e.New(e.CodeUsage, "Definition selects no applicable local native producers")
	}
	files["definition.json"], files["environment.json"] = definition, environment
	// Validate the whole derived graph before creating any output.
	_, err = compileDocumentsWithLoader(ctx, prepared.Definition, definition, prepared.Environment, environment, "local-native-preparation", func(ctx context.Context, selection authored.Catalog) (*catalog.Stack, error) {
		for _, output := range definition.Outputs {
			if output.Catalog != nil && slices.Equal(selection.Overlays, output.Catalog.Overlays) && selection.Base == output.Catalog.Base {
				return stacks[output.Name], nil
			}
		}
		return loadFactoryCatalogStack(ctx, prepared.Definition, selection)
	})
	if err != nil {
		return preparedLocalInputs{}, err
	}
	encoded := make(map[string][]byte, len(files))
	for name, value := range files {
		raw, err := canonical.Marshal(value)
		if err != nil {
			return preparedLocalInputs{}, err
		}
		encoded[name] = raw
	}
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return preparedLocalInputs{}, e.Wrap(e.CodeBuildFailed, err, "creating local input parent")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return preparedLocalInputs{}, e.Wrap(e.CodeBuildFailed, err, "creating fresh local input directory")
	}
	names := make([]string, 0, len(encoded))
	for name := range encoded {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := writeFactoryFile(ctx, filepath.Join(out, name), encoded[name], 0600); err != nil {
			return preparedLocalInputs{}, err
		}
	}
	return prepared, nil
}
