// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// DefinitionInit selects the one executable output of a starter Definition.
// Every field is explicit: nothing is defaulted and no dependency is added.
type DefinitionInit struct {
	Name       string
	Catalog    string
	Platform   string
	Components []string
}

// InitDefinition authors a plain Definition with one executable output named
// after the request. The catalog, platform and components are admitted through
// the same owners a later run uses, so a selection that cannot build is refused
// here instead of being written down. The catalog locator is resolved to an
// absolute path so that moving the emitted file cannot change what it means.
func InitDefinition(ctx context.Context, request DefinitionInit) (Definition, error) {
	if request.Name == "" || request.Catalog == "" || request.Platform == "" || len(request.Components) == 0 {
		return Definition{}, e.New(e.CodeUsage, "definition init requires --name, --catalog, --platform and at least one --component")
	}
	base, err := filepath.Abs(request.Catalog)
	if err != nil {
		return Definition{}, e.Wrap(e.CodeUsage, err, "resolving catalog %q", request.Catalog)
	}
	components := slices.Clone(request.Components)
	slices.Sort(components)
	for i := 1; i < len(components); i++ {
		if components[i] == components[i-1] {
			return Definition{}, e.New(e.CodeUsage, "component %q is selected more than once", components[i])
		}
	}
	authored := Definition{
		APIVersion: definition.DefinitionAPIVersion,
		Kind:       definition.DefinitionKind,
		Metadata:   definition.Metadata{Name: request.Name},
		Catalog:    definition.Catalog{Base: base},
		Outputs: []DefinitionOutput{{
			Name:       request.Name,
			Platform:   request.Platform,
			Components: components,
			Format:     definition.OutputExecutable,
		}},
	}
	// Shape first, so a bad name or platform never reads the catalog.
	if err := authored.Validate(); err != nil {
		return Definition{}, err
	}
	loaded, err := catalog.LoadContext(ctx, catalog.Dir(base))
	if err != nil {
		return Definition{}, err
	}
	if _, err := buildgraph.Resolve(loaded, buildgraph.Selection{
		Components: components, Platform: request.Platform, OutputFormat: definition.OutputExecutable,
	}); err != nil {
		return Definition{}, err
	}
	return authored, nil
}

// RenderDefinition returns the authored YAML of a validated Definition. Keys
// are sorted, so equal Definitions always produce identical bytes.
func RenderDefinition(authored Definition) ([]byte, error) {
	if err := authored.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(authored)
}
