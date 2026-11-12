// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// FragmentDataProvider admits explicit, digest-pinned sibling inputs. Providers
// must retain their confinement boundary, cancellation and no-follow guarantees.
// Snapshot providers resolve these inputs exclusively inside their captured root.
type FragmentDataProvider interface {
	ReadFragmentContext(context.Context, string) ([]byte, error)
}

type fragmentImport struct {
	Path        string            `json:"path"`
	SHA256      string            `json:"sha256"`
	Sources     map[string]string `json:"sources,omitempty"` // local name -> fragment name
	Components  map[string]string `json:"components,omitempty"`
	SourceNames map[string]string `json:"sourceNames,omitempty"` // component source binding
}

// Fragments contain declarations only: no metadata, locks, nested imports or
// overrides. Consumers retain exact local pin ownership and complete validation.
type registryFragment struct {
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	Sources    map[string]Source       `json:"sources,omitempty"`
	Components map[string]rawComponent `json:"components,omitempty"`
}

// ValidateFragmentPath checks a canonical relative reference before provider I/O.
// Parent segments are permitted only as a leading prefix to reach sibling inputs.
func ValidateFragmentPath(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || pathDepth(name) > MaxCatalogTraversalDepth {
		return e.New(e.CodeInvalidCatalog, "unsafe catalog fragment path %q", name)
	}
	rest := name
	for strings.HasPrefix(rest, "../") {
		rest = strings.TrimPrefix(rest, "../")
	}
	return validateCatalogPath(rest)
}

func (provider dirProvider) ReadFragmentContext(ctx context.Context, name string) ([]byte, error) {
	if err := ValidateFragmentPath(name); err != nil {
		return nil, err
	}
	if err := requireCatalogContext(ctx, "catalog fragment read"); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(provider.root)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "resolving fragment root")
	}
	for strings.HasPrefix(name, "../") {
		root = filepath.Dir(root)
		name = strings.TrimPrefix(name, "../")
	}
	return readCatalogRegularBeneathContext(ctx, root, name)
}

func (provider *boundContextProvider) readFragment(name string) ([]byte, error) {
	if err := ValidateFragmentPath(name); err != nil {
		return nil, err
	}
	if err := requireCatalogContext(provider.ctx, "catalog fragment read"); err != nil {
		return nil, err
	}
	reader, ok := provider.provider.(FragmentDataProvider)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "catalog provider %q does not support fragments", provider.Name())
	}
	raw, err := reader.ReadFragmentContext(provider.ctx, name)
	if err != nil {
		return nil, normalizeCatalogContextError(provider.ctx,
			e.Wrap(e.CodeInvalidCatalog, err, "reading catalog fragment %q", name), "catalog fragment read")
	}
	if err := ValidateFilePayload(path.Base(name), raw); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.totalBytes+int64(len(raw)) > MaxCatalogTotalBytes {
		return nil, e.New(e.CodeInvalidCatalog, "catalog fragments exceed the aggregate byte bound")
	}
	provider.totalBytes += int64(len(raw))
	return append([]byte(nil), raw...), nil
}

func expandFragments(reg *registryDoc, providers []DataProvider) error {
	if len(reg.Imports) == 0 {
		return nil
	}
	if len(reg.Imports) > 64 {
		return e.New(e.CodeInvalidCatalog, "catalog exceeds the 64-fragment bound")
	}
	selections := 0
	for _, imp := range reg.Imports {
		selections += len(imp.Sources) + len(imp.Components)
	}
	if selections > 1024 {
		return e.New(e.CodeInvalidCatalog, "catalog exceeds the 1024-fragment-selection bound")
	}
	if len(providers) != 1 {
		return e.New(e.CodeEngineCapability, "catalog fragments require a context-bound provider")
	}
	reader, ok := providers[0].(*boundContextProvider)
	if !ok {
		return e.New(e.CodeEngineCapability, "catalog fragments require a context-bound provider")
	}
	if reg.Sources == nil {
		reg.Sources = map[string]Source{}
	}
	if reg.Components == nil {
		reg.Components = map[string]rawComponent{}
	}
	for _, imp := range reg.Imports {
		if len(imp.SHA256) != 64 || strings.Trim(imp.SHA256, "0123456789abcdef") != "" || len(imp.Sources)+len(imp.Components) == 0 {
			return e.New(e.CodeInvalidCatalog, "fragment %q needs a lowercase SHA-256 and explicit selections", imp.Path)
		}
		raw, err := reader.readFragment(imp.Path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != imp.SHA256 {
			return e.New(e.CodeInvalidCatalog, "fragment %q bytes do not match sha256", imp.Path)
		}
		var fragment registryFragment
		if err := yaml.UnmarshalStrict(raw, &fragment); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "decoding fragment %q", imp.Path)
		}
		if fragment.APIVersion != "nvkr.dev/catalog-fragment/v1" || fragment.Kind != "CatalogFragment" {
			return e.New(e.CodeInvalidCatalog, "fragment %q has unsupported apiVersion or kind", imp.Path)
		}
		for _, local := range sortedSelections(imp.Sources) {
			value, exists := fragment.Sources[imp.Sources[local]]
			if !exists {
				return e.New(e.CodeInvalidCatalog, "fragment %q has no source %q", imp.Path, imp.Sources[local])
			}
			if _, duplicate := reg.Sources[local]; duplicate {
				return e.New(e.CodeInvalidCatalog, "fragment source %q collides with another declaration", local)
			}
			reg.Sources[local] = value
		}
		usedBindings := map[string]bool{}
		for _, local := range sortedSelections(imp.Components) {
			value, exists := fragment.Components[imp.Components[local]]
			if !exists {
				return e.New(e.CodeInvalidCatalog, "fragment %q has no component %q", imp.Path, imp.Components[local])
			}
			if _, duplicate := reg.Components[local]; duplicate {
				return e.New(e.CodeInvalidCatalog, "fragment component %q collides with another declaration", local)
			}
			if len(value.Patches) != 0 {
				return e.New(e.CodeInvalidCatalog, "fragment component %q cannot contain catalog-relative patches", local)
			}
			if source, bound := imp.SourceNames[value.Source]; bound {
				if value.Source == "" || source == "" {
					return e.New(e.CodeInvalidCatalog, "fragment component %q has an empty source binding", local)
				}
				usedBindings[value.Source] = true
				value.Source = source
			}
			reg.Components[local] = value
		}
		for _, source := range sortedSelections(imp.SourceNames) {
			if !usedBindings[source] {
				return e.New(e.CodeInvalidCatalog, "fragment %q has unused source binding %q", imp.Path, source)
			}
		}
	}
	return nil
}

func sortedSelections(selections map[string]string) []string {
	names := make([]string, 0, len(selections))
	for name := range selections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
