// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"maps"
	"path"
	"slices"
	"strings"

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
func DescribeStackContext(ctx context.Context, root, directory string, overlayPaths []string) (StackDescription, error) {
	provider, err := BindContext(ctx, inspectionProvider{source: Dir(root).(ContextDataProvider), directory: directory})
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
		raw, err := Dir(root).(ContextDataProvider).ReadFileContext(ctx, path)
		if err != nil {
			return StackDescription{}, e.Wrap(e.CodeInvalidCatalog, err, "reading inspection overlay")
		}
		overlay, err := DecodeOverlay(raw)
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
func DescribeFragmentContext(ctx context.Context, root, name string) (FragmentDescription, error) {
	provider, err := BindContext(ctx, inspectionProvider{source: Dir(root).(ContextDataProvider), directory: path.Dir(name)})
	if err != nil {
		return FragmentDescription{}, err
	}
	raw, err := provider.ReadFile(path.Base(name))
	if err != nil {
		return FragmentDescription{}, e.Wrap(e.CodeInvalidCatalog, err, "reading fragment")
	}
	raw, err = expandCommandFiles(raw, path.Base(name), []DataProvider{provider})
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

// inspectionProvider confines every loader read, including a changed registry's
// imports, beneath the explicit root. Preflight diagnostics alone cannot enforce
// this boundary because files may change between scanning and loader admission.
type inspectionProvider struct {
	source    ContextDataProvider
	directory string
}

func (p inspectionProvider) Name() string { return "inspection:" + p.directory }
func (p inspectionProvider) ReadFile(string) ([]byte, error) {
	return nil, e.New(e.CodeEngineCapability, "inspection requires context-bound reads")
}
func (p inspectionProvider) ReadDir(string) ([]string, error) {
	return nil, e.New(e.CodeEngineCapability, "inspection requires context-bound reads")
}
func (p inspectionProvider) resolve(name string) (string, error) {
	if p.directory != "." {
		if err := validateCatalogPath(p.directory); err != nil {
			return "", err
		}
	}
	if err := ValidateFragmentPath(name); err != nil {
		return "", err
	}
	resolved := path.Join(p.directory, name)
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return "", e.New(e.CodeInvalidCatalog, "inspection reference %q leaves the explicit input root", name)
	}
	return resolved, nil
}
func (p inspectionProvider) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	resolved, err := p.resolve(name)
	if err != nil {
		return nil, err
	}
	return p.source.ReadFileContext(ctx, resolved)
}
func (p inspectionProvider) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	resolved, err := p.resolve(name)
	if err != nil {
		return nil, err
	}
	return p.source.ReadDirContext(ctx, resolved)
}
func (p inspectionProvider) ReadFragmentContext(ctx context.Context, name string) ([]byte, error) {
	return p.ReadFileContext(ctx, name)
}
