// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// FamilyPolicy names one release cohort across explicit catalog source roles.
// Equal repository URLs or current commits never enroll another catalog.
// Sources retain their own fetch, patch, module and packaging contracts.
type FamilyPolicy struct {
	Name     string          `json:"name"`
	Catalogs []FamilyCatalog `json:"catalogs"`
}

type FamilyCatalog struct {
	Catalog string   `json:"catalog"`
	Sources []string `json:"sources"`
}

func (family FamilyPolicy) validate(primary string, primarySources []string) error {
	if !safeName.MatchString(family.Name) || len(family.Name) > 64 || len(family.Catalogs) == 0 || len(family.Catalogs) > 64 {
		return e.New(e.CodeUsage, "catalog family requires a bounded name and 1–64 catalog selections")
	}
	seen := map[string]bool{}
	primaryFound := false
	total := 0
	for _, member := range family.Catalogs {
		if !safeRelative(member.Catalog) || seen[member.Catalog] || len(member.Sources) == 0 || len(member.Sources) > 128 {
			return e.New(e.CodeUsage, "catalog family requires unique relative catalogs and 1–128 explicit source roots per catalog")
		}
		seen[member.Catalog] = true
		sources := map[string]bool{}
		for _, source := range member.Sources {
			if !safeName.MatchString(source) || sources[source] {
				return e.New(e.CodeUsage, "catalog family has an invalid or repeated source %q in %s", source, member.Catalog)
			}
			sources[source] = true
		}
		total += len(member.Sources)
		if member.Catalog == primary {
			primaryFound = true
			for _, source := range primarySources {
				if !slices.Contains(member.Sources, source) {
					return e.New(e.CodeUsage, "catalog family omits primary maintenance source %q", source)
				}
			}
		}
	}
	if !primaryFound || total > 256 {
		return e.New(e.CodeUsage, "catalog family must include primary maintenance roots and at most 256 source roles")
	}
	return nil
}
