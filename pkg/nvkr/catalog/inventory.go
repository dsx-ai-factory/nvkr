// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// InventoryCatalog describes one catalog beneath the requested root. Path is
// root-relative, including "." for the root itself. Recorded pins are not
// authenticated upstream evidence; in particular pin sets are not re-derived.
type InventoryCatalog struct {
	Path       string                 `json:"path"`
	Name       string                 `json:"name"`
	Components []ComponentDescription `json:"components"`
}

// InventoryContext scans a leaf or catalog root in stable path/name order.
// It validates every local registry, import and lock before returning any
// descriptions. It never fetches upstream trees, updates pins, or returns a
// Catalog that callers could use for executable selection.
func InventoryContext(ctx context.Context, root string) ([]InventoryCatalog, error) {
	if err := requireCatalogContext(ctx, "catalog inventory"); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, e.New(e.CodeEngineCapability, "bounded catalog inventory requires Linux or macOS")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "resolving catalog inventory root")
	}
	if root == "" {
		return nil, e.New(e.CodeUsage, "catalog inventory requires an explicit catalog root")
	}
	// Reuse the catalog provider's bounded, no-follow recursive directory walk.
	files, err := readCatalogInventoryDirectoryContext(ctx, abs)
	if err != nil {
		return nil, normalizeCatalogContextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "scanning catalog root %q", root), "catalog inventory")
	}
	result := []InventoryCatalog{}
	for _, file := range files {
		if path.Base(file) != RegistryFile {
			continue
		}
		if err := requireCatalogContext(ctx, "catalog inventory"); err != nil {
			return nil, err
		}
		rel := path.Dir(file)
		provider, err := BindContext(ctx, Dir(filepath.Join(abs, filepath.FromSlash(rel))))
		if err != nil {
			return nil, err
		}
		entry, err := inventoryCatalog(ctx, provider, rel)
		if err != nil {
			return nil, normalizeCatalogContextError(ctx, err, "catalog inventory")
		}
		result = append(result, entry)
	}
	if len(result) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "no catalog registry under %q", root)
	}
	slices.SortFunc(result, func(a, b InventoryCatalog) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

func inventoryCatalog(ctx context.Context, provider DataProvider, relative string) (InventoryCatalog, error) {
	reg, err := readRegistry(provider)
	if err != nil {
		return InventoryCatalog{}, err
	}
	lock, err := readLock(provider)
	if err != nil {
		return InventoryCatalog{}, err
	}
	if err := validateLockAgainstRegistry(reg, lock, true); err != nil {
		return InventoryCatalog{}, err
	}
	loaded, err := resolveRecordedCatalog(ctx, provider, reg, lock)
	if err != nil {
		return InventoryCatalog{}, err
	}
	entry := InventoryCatalog{Path: relative, Name: loaded.Metadata.Name, Components: []ComponentDescription{}}
	for _, name := range slices.Sorted(maps.Keys(loaded.Components)) {
		if err := requireCatalogContext(ctx, "catalog inventory"); err != nil {
			return InventoryCatalog{}, err
		}
		description, err := loaded.Describe(name)
		if err != nil {
			return InventoryCatalog{}, err
		}
		entry.Components = append(entry.Components, description)
	}
	return entry, nil
}

// FilterInventory preserves separate catalog locations and never selects a
// producer. Empty selectors keep every component and every (even empty) catalog.
func FilterInventory(entries []InventoryCatalog, name, platform, outputType string) ([]InventoryCatalog, error) {
	if err := ValidateSelectors(platform, outputType); err != nil {
		return nil, err
	}
	result := []InventoryCatalog{}
	for _, entry := range entries {
		filtered := InventoryCatalog{Path: entry.Path, Name: entry.Name, Components: []ComponentDescription{}}
		for _, component := range entry.Components {
			if name != "" && component.Name != name {
				continue
			}
			if platform != "" && !slices.Contains(component.Platforms, platform) {
				continue
			}
			if outputType != "" && component.OutputType != outputType {
				continue
			}
			filtered.Components = append(filtered.Components, component)
		}
		if len(filtered.Components) != 0 || (name == "" && platform == "" && outputType == "") {
			result = append(result, filtered)
		}
	}
	if len(result) == 0 {
		return nil, e.New(e.CodeNoMatch, "no catalog component matches name=%q platform=%q output-type=%q", name, platform, outputType)
	}
	return result, nil
}

// RenderInventoryIndex emits the generated Markdown index from the same
// descriptions as list/find. Quoting and HTML escaping prevent authored strings
// from introducing Markdown structure or terminal control sequences.
func RenderInventoryIndex(entries []InventoryCatalog) []byte {
	var out bytes.Buffer
	out.WriteString("# Catalog inventory\n\nGenerated by `make catalog-index-generate`; do not edit. Paths are relative to this directory.\n\nRecorded declarations only; no producer selection or upstream pin-set verification. Use `catalog find` for recorded pins.\n\n| Catalog path | Component | Producer |\n|---|---|---|\n")
	cell := func(value string) string {
		value = strconv.Quote(value)
		value = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "&#124;", "`", "&#96;", "\\", "&#92;", "*", "&#42;", "_", "&#95;", "[", "&#91;").Replace(value)
		return "<code>" + value + "</code>"
	}
	for _, entry := range entries {
		if len(entry.Components) == 0 {
			fmt.Fprintf(&out, "| %s | (empty) | |\n", cell(entry.Path))
		}
		for _, component := range entry.Components {
			fmt.Fprintf(&out, "| %s | %s | %s |\n", cell(entry.Path), cell(component.Name), cell(component.Mode))
		}
	}
	return out.Bytes()
}
