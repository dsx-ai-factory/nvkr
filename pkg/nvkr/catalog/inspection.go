// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"maps"
	"path/filepath"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// ImportDescription records exact authored selections, not execution authority.
type ImportDescription struct {
	Path        string            `json:"path"`
	SHA256      string            `json:"sha256"`
	Sources     map[string]string `json:"sources,omitempty"`
	Components  map[string]string `json:"components,omitempty"`
	SourceNames map[string]string `json:"sourceNames,omitempty"`
}

// StackDescription describes locally checked bytes and ordered predecessor joins.
// It deliberately exposes no executable Catalog. Derived upstream pin sets are
// not fetched or re-derived by offline inspection.
type StackDescription struct {
	Name       string              `json:"name"`
	Digest     string              `json:"catalogDigest"`
	Components []string            `json:"components"`
	Imports    []ImportDescription `json:"imports,omitempty"`
	Overlays   []AppliedOverlay    `json:"overlays,omitempty"`
}

// DescribeStackContext validates recorded local inputs through the ordinary
// registry, lock, component and overlay loaders, without upstream resolution.
func DescribeStackContext(ctx context.Context, directory string, overlayPaths []string) (StackDescription, error) {
	provider, err := BindContext(ctx, Dir(directory))
	if err != nil {
		return StackDescription{}, err
	}
	reg, err := readRegistry(provider)
	if err != nil {
		return StackDescription{}, err
	}
	lock, err := readLock(provider)
	if err != nil {
		return StackDescription{}, err
	}
	if err := validateLockAgainstRegistry(reg, lock, true); err != nil {
		return StackDescription{}, err
	}
	base, err := resolveRecordedCatalog(ctx, provider, reg, lock)
	if err != nil {
		return StackDescription{}, err
	}
	overlays := make([]Overlay, 0, len(overlayPaths))
	for _, path := range overlayPaths {
		overlay, err := LoadOverlayContext(ctx, path)
		if err != nil {
			return StackDescription{}, err
		}
		overlays = append(overlays, overlay)
	}
	stack, err := ApplyStack(base, overlays...)
	if err != nil {
		return StackDescription{}, err
	}
	result := StackDescription{Name: base.Metadata.Name, Digest: stack.Digest,
		Components: slices.Sorted(maps.Keys(stack.Catalog.Components)), Overlays: stack.Applied}
	for _, imp := range reg.Imports {
		result.Imports = append(result.Imports, ImportDescription{Path: imp.Path, SHA256: imp.SHA256,
			Sources: maps.Clone(imp.Sources), Components: maps.Clone(imp.Components), SourceNames: maps.Clone(imp.SourceNames)})
	}
	return result, nil
}

// FragmentDescription lists declarations. Complete producer validation still
// requires an importing registry's explicit mappings and independently owned pins.
type FragmentDescription struct {
	Sources    []string `json:"sources"`
	Components []string `json:"components"`
}

// DescribeFragmentContext admits the fragment's closed declaration schema using
// the same command-file expansion and raw declaration types as catalog loading.
func DescribeFragmentContext(ctx context.Context, path string) (FragmentDescription, error) {
	provider, err := BindContext(ctx, Dir(filepath.Dir(path)))
	if err != nil {
		return FragmentDescription{}, err
	}
	raw, err := provider.ReadFile(filepath.Base(path))
	if err != nil {
		return FragmentDescription{}, e.Wrap(e.CodeInvalidCatalog, err, "reading fragment")
	}
	raw, err = expandCommandFiles(raw, filepath.Base(path), []DataProvider{provider})
	if err != nil {
		return FragmentDescription{}, err
	}
	var fragment registryFragment
	if err := yaml.UnmarshalStrict(raw, &fragment); err != nil {
		return FragmentDescription{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding fragment")
	}
	if fragment.APIVersion != "nvkr.dev/catalog-fragment/v1" || fragment.Kind != "CatalogFragment" {
		return FragmentDescription{}, e.New(e.CodeInvalidCatalog, "fragment has unsupported apiVersion or kind")
	}
	return FragmentDescription{Sources: slices.Sorted(maps.Keys(fragment.Sources)), Components: slices.Sorted(maps.Keys(fragment.Components))}, nil
}
