// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"path"
	"path/filepath"
	"slices"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// OutputAttribution names the member that declared one effective output.
type OutputAttribution struct {
	Output string `json:"output"`
	Member string `json:"member"`
}

// Effective returns an independent copy of the derived v5 Definition. It is an
// internal record: it has no file and is never an authored input.
func (composition Composition) Effective() Definition { return composition.effective.Clone() }

// Attribution returns every effective output's declaring member, sorted by output.
func (composition Composition) Attribution() []OutputAttribution {
	return slices.Clone(composition.attribution)
}

// mergeComposite derives the effective Definition by ADR-26 §3: an exact
// disjoint union of member outputs with every catalog stack rebased to the
// composite's directory. It never applies precedence, defaults or renaming.
func mergeComposite(compositePath string, metadata Metadata, admitted []AdmittedMember) (Definition, []OutputAttribution, error) {
	members := slices.Clone(admitted)
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	effective := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: metadata}
	owners := map[string]string{}
	for i, member := range members {
		source := member.Definition.Clone()
		stack, err := rebaseCatalog(member.Path, compositePath, source.Catalog)
		if err != nil {
			return Definition{}, nil, e.Wrap(e.CodeInvalidCatalog, err, "composite member %q catalog", member.Name)
		}
		if i == 0 {
			effective.Catalog, effective.SBOMTool, effective.CosignTool = stack, source.SBOMTool, source.CosignTool
		} else {
			if !sameCatalog(effective.Catalog, stack) {
				return Definition{}, nil, e.New(e.CodeInvalidCatalog, "composite member %q top-level catalog stack differs from member %q", member.Name, members[0].Name)
			}
			if source.SBOMTool != effective.SBOMTool || source.CosignTool != effective.CosignTool {
				return Definition{}, nil, e.New(e.CodeInvalidCatalog, "composite member %q selects different sbomTool or cosignTool than member %q", member.Name, members[0].Name)
			}
		}
		for _, output := range source.Outputs {
			if owner, repeated := owners[output.Name]; repeated {
				return Definition{}, nil, e.New(e.CodeInvalidCatalog, "composite output %q is declared by members %q and %q", output.Name, owner, member.Name)
			}
			owners[output.Name] = member.Name
			outputStack := stack
			if output.Catalog != nil {
				if outputStack, err = rebaseCatalog(member.Path, compositePath, *output.Catalog); err != nil {
					return Definition{}, nil, e.Wrap(e.CodeInvalidCatalog, err, "composite member %q output %q catalog", member.Name, output.Name)
				}
			}
			output.Catalog = &outputStack
			effective.Outputs = append(effective.Outputs, output)
		}
	}
	sort.Slice(effective.Outputs, func(i, j int) bool { return effective.Outputs[i].Name < effective.Outputs[j].Name })
	attribution := make([]OutputAttribution, 0, len(effective.Outputs))
	for _, output := range effective.Outputs {
		attribution = append(attribution, OutputAttribution{Output: output.Name, Member: owners[output.Name]})
	}
	if err := effective.Validate(); err != nil {
		return Definition{}, nil, err
	}
	return effective, attribution, nil
}

// rebaseCatalog moves member-relative catalog locators to the composite's
// directory. Each resolved locator must stay inside the admitted input root.
func rebaseCatalog(memberPath, compositePath string, stack Catalog) (Catalog, error) {
	base, err := rebaseLocator(memberPath, compositePath, stack.Base, true)
	if err != nil {
		return Catalog{}, err
	}
	rebased := Catalog{Base: base}
	for _, overlay := range stack.Overlays {
		value, err := rebaseLocator(memberPath, compositePath, overlay, false)
		if err != nil {
			return Catalog{}, err
		}
		rebased.Overlays = append(rebased.Overlays, value)
	}
	return rebased, nil
}

func rebaseLocator(memberPath, compositePath, value string, directory bool) (string, error) {
	resolved, err := ResolveInputPath(memberPath, value, directory)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(filepath.FromSlash(path.Dir(compositePath)), filepath.FromSlash(resolved))
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "rebasing %q", value)
	}
	return filepath.ToSlash(relative), nil
}

func sameCatalog(left, right Catalog) bool {
	return left.Base == right.Base && slices.Equal(left.Overlays, right.Overlays)
}
