// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// BindSourceProducer resolves a complete authored producer contract against one
// explicit catalog producer's source pin. It derives a description for overlay
// generation, never execution authority. Fetch, patch and packaging declarations
// remain those of the complete template; no general field merge is performed.
func (cat *Catalog) BindSourceProducer(name string, template Component) (Component, error) {
	if cat == nil {
		return Component{}, e.New(e.CodeInvalidCatalog, "source binding requires a catalog")
	}
	if err := cat.Validate(); err != nil {
		return Component{}, err
	}
	source, ok := cat.Components[name]
	if !ok || !source.FromSource || !template.FromSource || template.Prebuilt != nil {
		return Component{}, e.New(e.CodeInvalidCatalog, "source binding requires explicit from-source producers")
	}
	if template.Repo != "" || template.Tag != "" || template.Commit != "" || template.TreeHash != "" || template.SourceDate != 0 {
		return Component{}, e.New(e.CodeInvalidCatalog, "source-bound producer must not author repository or pin values")
	}
	template.Repo, template.Tag, template.Commit, template.TreeHash, template.SourceDate = source.Repo, source.Tag, source.Commit, source.TreeHash, source.SourceDate
	stamps, err := renderStamps(template.VersionStamp, Pin{Tag: source.Tag, Commit: source.Commit, TreeHash: source.TreeHash, SourceDateEpoch: source.SourceDate})
	if err != nil {
		return Component{}, e.Wrap(e.CodeInvalidCatalog, err, "rendering source-bound version stamps")
	}
	template.VersionStamp = stamps
	template.SourceRecipe = cloneSourceRecipe(template.SourceRecipe)
	if err := template.resolveSourceRecipe(); err != nil {
		return Component{}, err
	}
	if err := validateComponent(name, template); err != nil {
		return Component{}, err
	}
	return template, nil
}
