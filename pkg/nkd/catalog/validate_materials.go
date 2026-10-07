// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
)

func validateMaterialBindings(name string, component Component) error {
	if len(component.Materials) == 0 {
		return nil
	}
	if !component.FromSource {
		return e.New(e.CodeInvalidCatalog,
			"component %q: material bindings require a from-source component", name)
	}
	if component.BuilderType != "" && component.BuilderType != BuilderVMImage && component.BuilderType != BuilderOCIHermetic {
		return e.New(e.CodeInvalidCatalog,
			"component %q: material bindings require builderType oci-hermetic or vm-image", name)
	}
	boundComponents := make(map[string]string, len(component.Materials))
	for _, binding := range sortedStringKeys(component.Materials) {
		materialComponent := component.Materials[binding]
		if _, err := material.StagingPath(binding); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"component %q has invalid material binding name", name)
		}
		if !validPackerTransportValue(materialComponent, maxMaterialComponentBytes) ||
			materialComponent != strings.TrimSpace(materialComponent) {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material binding %q has invalid component name %q", name, binding, materialComponent)
		}
		if materialComponent == name {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material binding %q refers to itself", name, binding)
		}
		if previous, duplicate := boundComponents[materialComponent]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material bindings %q and %q ambiguously select component %q",
				name, previous, binding, materialComponent)
		}
		boundComponents[materialComponent] = binding
	}
	return nil
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ValidateMaterialSet verifies one output's complete selected component set. It
// validates declarations rather than authenticating artifact bytes; execution must
// bind each edge to a verified producer observation before staging.
func ValidateMaterialSet(components []NamedComponent) error {
	selected := make(map[string]Component, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.Name) == "" || component.Name != strings.TrimSpace(component.Name) {
			return e.New(e.CodeInvalidCatalog,
				"resolved material set has invalid component name %q", component.Name)
		}
		if _, duplicate := selected[component.Name]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"resolved material set repeats component %q", component.Name)
		}
		if err := validateComponent(component.Name, component.Component); err != nil {
			return err
		}
		selected[component.Name] = component.Component
	}
	return validateMaterialGraph(selected)
}

func validateMaterialGraph(components map[string]Component) error {
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, binding := range sortedStringKeys(components[name].Materials) {
			targetName := components[name].Materials[binding]
			target, ok := components[targetName]
			if !ok {
				return e.New(e.CodeInvalidCatalog,
					"component %q material binding %q references unknown component %q", name, binding, targetName)
			}
			if !target.FromSource && target.Prebuilt == nil {
				return e.New(e.CodeInvalidCatalog,
					"component %q material binding %q references excluded component %q", name, binding, targetName)
			}
		}
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(components))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return e.New(e.CodeInvalidCatalog, "material dependency cycle includes component %q", name)
		case visited:
			return nil
		}
		state[name] = visiting
		for _, binding := range sortedStringKeys(components[name].Materials) {
			if err := visit(components[name].Materials[binding]); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}
