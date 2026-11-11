// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BundleRootName admits either the original single source archive or exactly one
// pinned-tool layout and its explicitly selected regular-file material closure.
func BundleRootName(components []catalog.NamedComponent) (string, error) {
	if err := catalog.ValidateMaterialSet(components); err != nil {
		return "", err
	}
	if len(components) == 1 {
		c := components[0]
		if c.FromSource && c.Prebuilt == nil && c.OutputType == "tarball" {
			return c.Name, nil
		}
	}
	selected := make(map[string]catalog.Component, len(components))
	root := ""
	for _, c := range components {
		if _, exists := selected[c.Name]; exists {
			return "", e.New(e.CodeInvalidCatalog, "duplicate bundle producer %q", c.Name)
		}
		selected[c.Name] = c.Component
		if c.BuilderType == catalog.BuilderPinnedTool && c.OutputType == catalog.PinnedToolOutputOCILayout {
			if root != "" {
				return "", e.New(e.CodeInvalidCatalog, "bundle output selects multiple layout roots")
			}
			root = c.Name
		}
	}
	if root == "" {
		return "", e.New(e.CodeInvalidCatalog, "bundle output requires one source archive or pinned-tool layout root")
	}
	reached := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if reached[name] {
			return nil
		}
		c, ok := selected[name]
		if !ok {
			return e.New(e.CodeInvalidCatalog, "bundle material %q is unselected", name)
		}
		if name != root {
			switch c.OutputType {
			case catalog.PinnedToolOutputFile, "raw", "tarball", "host-package":
			default:
				return e.New(e.CodeEngineCapability, "bundle material %q is not an admitted regular file", name)
			}
		}
		reached[name] = true
		for _, dependency := range c.Materials {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return "", err
	}
	if len(reached) != len(selected) {
		return "", e.New(e.CodeInvalidCatalog, "bundle output selects producers outside root %q material closure", root)
	}
	return root, nil
}
