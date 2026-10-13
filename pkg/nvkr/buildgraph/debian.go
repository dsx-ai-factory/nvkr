// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func resolveDebianSelection(cat *catalog.Catalog, selection Selection) (*Resolved, error) {
	component, ok := cat.Components[selection.Components[0]]
	if !ok || component.OutputType != catalog.OutputDebianPackage || component.Debian == nil {
		return nil, e.New(e.CodeInvalidCatalog, "Debian output must select exactly one explicit Debian source producer")
	}
	request := target.Debian{Platform: selection.Platform}
	if selection.Debian != nil {
		request.Distribution = selection.Debian.Distribution
		if request.Platform == "" {
			request.Platform = selection.Debian.Platform
		}
	}
	routes := make([]target.Debian, 0, len(component.Debian.Routes))
	for _, route := range component.Debian.Routes {
		routes = append(routes, route.Target)
	}
	resolved, err := target.ResolveDebian(request, nil, routes)
	if err != nil {
		return nil, err
	}
	return &Resolved{Platform: resolved.Platform, Debian: &resolved,
		Components: []catalog.NamedComponent{{Name: selection.Components[0], Component: cloneComponent(component)}},
	}, nil
}

// selectedDebianRoute is lowering of already revalidated description data. It
// preserves exact source argv and archive path; no package wrapper is added.
func selectedDebianRoute(component catalog.NamedComponent, selected *target.Debian) (catalog.NamedComponent, error) {
	if selected == nil || component.Debian == nil {
		return component, e.New(e.CodeInvalidCatalog, "Debian producer lacks its resolved target")
	}
	for _, route := range component.Debian.Routes {
		if route.Target == *selected {
			component.ToolchainImage = route.ToolchainImage
			component.ToolchainImages = nil
			component.BuildCommand = slices.Clone(route.BuildCommand)
			component.ArtifactPath, component.WriteDir = route.ArtifactPath, route.WriteDir
			return component, nil
		}
	}
	return component, e.New(e.CodeInvalidCatalog, "Debian producer has no exact route for the resolved target")
}
