// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// InternalTool selects an ordinary catalog producer and its explicit closure.
// Result names the component whose bytes a consumer uses. This description
// neither authorizes execution nor adds a delivered output or publication route.
type InternalTool struct {
	Name       string   `json:"name"`
	Platform   string   `json:"platform"`
	Catalog    *Catalog `json:"catalog,omitempty"`
	Components []string `json:"components"`
	Result     string   `json:"result"`
	Format     string   `json:"format"`
	SBOMTool   string   `json:"sbomTool,omitempty"`
}

func (definition Definition) validateInternalTools() error {
	previous := ""
	for _, tool := range definition.InternalTools {
		if !namePattern.MatchString(tool.Name) || tool.Name <= previous {
			return e.New(e.CodeInvalidCatalog, "internal tools require canonical, sorted, unique names")
		}
		previous = tool.Name
		if tool.SBOMTool != "" && !namePattern.MatchString(tool.SBOMTool) {
			return e.New(e.CodeInvalidCatalog, "internal tool %q has an invalid SBOM scanner selector", tool.Name)
		}
		if tool.Platform != "linux/amd64" && tool.Platform != "linux/arm64" {
			return e.New(e.CodeEngineCapability, "internal tool %q requires a native Linux platform", tool.Name)
		}
		if tool.Format != OutputExecutable && tool.Format != OutputOCI {
			return e.New(e.CodeEngineCapability, "internal tool %q has unsupported result form %q", tool.Name, tool.Format)
		}
		if tool.Catalog != nil && tool.Catalog.Base == "" {
			return e.New(e.CodeInvalidCatalog, "internal tool %q has an empty catalog override", tool.Name)
		}
		if len(tool.Components) == 0 || !namePattern.MatchString(tool.Result) || !slices.Contains(tool.Components, tool.Result) {
			return e.New(e.CodeInvalidCatalog, "internal tool %q requires its result in the explicit producer selection", tool.Name)
		}
		if err := sortedUnique("internal tool components", tool.Components); err != nil {
			return err
		}
		for _, component := range tool.Components {
			if !namePattern.MatchString(component) {
				return e.New(e.CodeInvalidCatalog, "internal tool %q selects invalid component %q", tool.Name, component)
			}
		}
		if tool.Format == OutputOCI && len(tool.Components) != 1 {
			return e.New(e.CodeInvalidCatalog, "internal OCI tool %q requires exactly one image producer", tool.Name)
		}
	}
	return nil
}
