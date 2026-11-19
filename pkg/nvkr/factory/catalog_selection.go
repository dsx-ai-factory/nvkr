// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CatalogCandidate binds one ordered authored locator to its resolved snapshot.
// It is descriptive Plan data, revalidated at every executable boundary.
type CatalogCandidate struct {
	Selection       authored.Catalog         `json:"selection"`
	CatalogDigest   string                   `json:"catalogDigest"`
	CatalogSnapshot *catalog.Catalog         `json:"catalogSnapshot"`
	AppliedOverlays []catalog.AppliedOverlay `json:"appliedOverlays,omitempty"`
}

func (candidate CatalogCandidate) stack() *catalog.Stack {
	return &catalog.Stack{Catalog: candidate.CatalogSnapshot, Digest: candidate.CatalogDigest, Applied: candidate.AppliedOverlays}
}

func loadCatalogCandidates(ctx context.Context, selections []authored.Catalog,
	load func(context.Context, authored.Catalog) (*catalog.Stack, error),
) ([]CatalogCandidate, error) {
	var candidates []CatalogCandidate
	for _, selection := range selections {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "catalog selection canceled")
		}
		stack, err := load(ctx, selection)
		if err != nil {
			return nil, err
		}
		if stack == nil || stack.Catalog == nil {
			return nil, e.New(e.CodeInvalidCatalog, "catalog %q has no snapshot", selection.Base)
		}
		copy, err := catalog.ApplyStack(stack.Catalog)
		if err != nil {
			return nil, err
		}
		if copy.Digest != stack.Digest {
			return nil, e.New(e.CodeUnverifiedRef, "catalog %q snapshot differs", selection.Base)
		}
		selection.Overlays = slices.Clone(selection.Overlays)
		candidates = append(candidates, CatalogCandidate{Selection: selection, CatalogDigest: stack.Digest,
			CatalogSnapshot: copy.Catalog, AppliedOverlays: slices.Clone(stack.Applied)})
	}
	return candidates, nil
}

// Select the complete explicit membership and platform set from one stack.
// Producer validity and format admission cannot be used as fallback criteria.
func selectCatalogCandidate(output authored.DefinitionOutput, candidates []CatalogCandidate) (CatalogCandidate, error) {
	match := -1
	for i, candidate := range candidates {
		fits := true
		for _, name := range output.Components {
			component, ok := candidate.CatalogSnapshot.Components[name]
			if !ok {
				fits = false
				break
			}
			for _, native := range nativeOutputSelections(output) {
				if !slices.Contains(component.Platforms, native.Platform) {
					fits = false
					break
				}
			}
			if !fits {
				break
			}
		}
		if !fits {
			continue
		}
		if match >= 0 {
			return CatalogCandidate{}, e.New(e.CodeAmbiguous, "output %q matches catalog stacks %q and %q; select an explicit catalog override", output.Name, candidates[match].Selection.Base, candidate.Selection.Base)
		}
		match = i
	}
	if match < 0 {
		return CatalogCandidate{}, e.New(e.CodeNoMatch, "output %q has no catalog containing its complete component and platform selection", output.Name)
	}
	return candidates[match], nil
}

func (plan Plan) validateCatalogCandidates() error {
	if len(plan.CatalogCandidates) != len(plan.Definition.Catalogs) {
		return e.New(e.CodeUnverifiedRef, "Plan catalog candidates differ from Definition")
	}
	for i, candidate := range plan.CatalogCandidates {
		if !sameCatalogSelection(candidate.Selection, plan.Definition.Catalogs[i]) || candidate.CatalogSnapshot == nil {
			return e.New(e.CodeUnverifiedRef, "Plan catalog candidate %d differs from Definition", i)
		}
		digest, err := candidate.CatalogSnapshot.SnapshotDigest()
		if err != nil {
			return err
		}
		previous := ""
		for _, applied := range candidate.AppliedOverlays {
			if !canonicalName.MatchString(applied.Name) || !canonical.IsDigest(applied.OverlayDigest) ||
				!canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) ||
				(previous != "" && applied.InputDigest != previous) {
				return e.New(e.CodeUnverifiedRef, "Plan catalog candidate %d has an invalid overlay chain", i)
			}
			previous = applied.OutputDigest
		}
		if previous != "" && previous != digest {
			return e.New(e.CodeUnverifiedRef, "Plan catalog candidate %d overlay chain ends at the wrong snapshot", i)
		}
		if digest != candidate.CatalogDigest {
			return e.New(e.CodeUnverifiedRef, "Plan catalog candidate %d snapshot differs", i)
		}
	}
	selections, err := plan.selectedCatalogs()
	if err != nil {
		return err
	}
	for _, build := range plan.Builds {
		spec, ok := selections[build.Name]
		if ok && (build.SelectedCatalog == nil || !sameCatalogSelection(*build.SelectedCatalog, spec)) || !ok && build.SelectedCatalog != nil {
			return e.New(e.CodeUnverifiedRef, "build %q selected catalog locator differs", build.Name)
		}
	}
	if len(plan.CatalogCandidates) == 0 {
		return nil
	}
	outputs := slices.Clone(plan.Definition.Outputs)
	for _, tool := range plan.Definition.InternalTools {
		outputs = append(outputs, internalToolSelection(tool))
	}
	for _, output := range outputs {
		if output.Catalog != nil {
			continue
		}
		candidate, err := selectCatalogCandidate(output, plan.CatalogCandidates)
		if err != nil {
			return err
		}
		for _, native := range nativeOutputSelections(output) {
			index := slices.IndexFunc(plan.Builds, func(build BuildPlan) bool { return build.Name == native.Name })
			if index < 0 || plan.Builds[index].CatalogDigest != candidate.CatalogDigest || !reflect.DeepEqual(plan.Builds[index].AppliedOverlays, candidate.AppliedOverlays) {
				return e.New(e.CodeUnverifiedRef, "build %q substitutes selected catalog", native.Name)
			}
		}
	}
	return nil
}

// selectedCatalogs rederives locators for whole output platform sets.
func (plan Plan) selectedCatalogs() (map[string]authored.Catalog, error) {
	selected := map[string]authored.Catalog{}
	if len(plan.Definition.Catalogs) == 0 {
		return selected, nil
	}
	outputs := slices.Clone(plan.Definition.Outputs)
	for _, tool := range plan.Definition.InternalTools {
		outputs = append(outputs, internalToolSelection(tool))
	}
	for _, output := range outputs {
		var spec authored.Catalog
		if output.Catalog != nil {
			spec = *output.Catalog
		} else {
			candidate, err := selectCatalogCandidate(output, plan.CatalogCandidates)
			if err != nil {
				return nil, err
			}
			spec = candidate.Selection
		}
		for _, native := range nativeOutputSelections(output) {
			spec.Overlays = slices.Clone(spec.Overlays)
			selected[native.Name] = spec
		}
	}
	return selected, nil
}

func sameCatalogSelection(a, b authored.Catalog) bool {
	return a.Base == b.Base && slices.Equal(a.Overlays, b.Overlays)
}
