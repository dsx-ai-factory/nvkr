// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// LoadContext performs a deep cancelable catalog load. Every registry, lock, patch,
// and directory read is routed through one context-bound provider with aggregate
// byte and directory-entry ceilings.
func LoadContext(ctx context.Context, p DataProvider) (*Catalog, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	loaded, err := load(ctx, bound)
	return loaded, normalizeCatalogContextError(ctx, err, "catalog load")
}

func load(ctx context.Context, p DataProvider) (*Catalog, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	lock, err := readLock(p)
	if err != nil {
		return nil, err
	}
	if err := validateLockAgainstRegistry(reg, lock, true); err != nil {
		return nil, err
	}

	if err := validateDerivedPins(ctx, reg, lock); err != nil {
		return nil, err
	}

	cat := &Catalog{
		APIVersion: reg.APIVersion,
		Kind:       reg.Kind,
		Metadata:   reg.Metadata,
		Sources:    reg.Sources,
		Components: map[string]Component{},
		Tools:      map[string]ToolPin{},
		Actions:    map[string]ActionPin{},
		sourcePins: make(map[string]Pin, len(lock.Pins)),
	}

	for name, pin := range lock.Pins {
		cat.sourcePins[name] = clonePin(pin)
		if source := reg.Sources[name]; source.Kind == SourceGitHubAction {
			action := ActionPin{Repo: strings.TrimPrefix(source.Repo, "https://github.com/"), Version: pin.Tag, SHA: pin.Commit}
			if err := action.Validate(); err != nil {
				return nil, err
			}
			cat.Actions[name] = action
		}
		if pin.Tool != nil {
			cat.Tools[name] = *pin.Tool
		}
	}

	// Components: resolve source + pin into the concrete Component.
	names := make([]string, 0, len(reg.Components))
	for n := range reg.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c, err := resolveComponent(name, reg.Components[name], reg.Sources, lock)
		if err != nil {
			return nil, err
		}
		if err := resolvePatches(p, name, &c, reg.Components[name].Patches); err != nil {
			return nil, err
		}
		cat.Components[name] = c
	}

	if err := cat.Validate(); err != nil {
		return nil, err
	}
	return cat, nil
}

func clonePin(pin Pin) Pin {
	if pin.Tool != nil {
		tool := *pin.Tool
		pin.Tool = &tool
	}
	return pin
}

// rejectLegacyComposition rejects stale recipe layer inputs rather than silently
// dropping them after migration. Provider enumeration retains bounded no-follow I/O.
func rejectLegacyComposition(p DataProvider) error {
	for _, directory := range []string{"overlays", "mixins"} {
		entries, err := p.ReadDir(directory)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "checking removed recipe directory %s", directory)
		}
		if len(entries) != 0 {
			return e.New(e.CodeInvalidCatalog, "legacy recipe %s directory is unsupported; select explicit component producers", directory)
		}
	}
	return nil
}

func readRegistry(p DataProvider) (*registryDoc, error) {
	if err := rejectLegacyComposition(p); err != nil {
		return nil, err
	}
	raw, err := p.ReadFile(RegistryFile)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s from %s", RegistryFile, p.Name())
	}
	return decodeRegistry(raw, fmt.Sprintf("%s from %s", RegistryFile, p.Name()))
}

func decodeRegistry(raw []byte, where string) (*registryDoc, error) {
	var reg registryDoc
	// Strict decode: an unknown or misplaced field is a coded error, never silently
	// dropped. Removed authoring fields and misspelled producer fields fail closed.
	if err := yaml.UnmarshalStrict(raw, &reg); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", where)
	}
	if reg.APIVersion != catalogAPIVersion {
		return nil, e.New(e.CodeInvalidCatalog, "catalog apiVersion %q is unsupported; want exactly %q", reg.APIVersion, catalogAPIVersion)
	}
	if reg.Kind != catalogKind {
		return nil, e.New(e.CodeInvalidCatalog, "catalog kind %q is unsupported; want exactly %q", reg.Kind, catalogKind)
	}
	if err := validateRegistryDeclarations(&reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func readLock(p DataProvider) (*PinLock, error) {
	raw, err := p.ReadFile(LockFile)
	if err != nil {
		return nil, e.Wrap(e.CodeUnpinnedSource, err, "required %s is missing or unreadable from %s", LockFile, p.Name())
	}
	return decodePinLock(raw, fmt.Sprintf("%s from %s", LockFile, p.Name()))
}
