// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFamilyPolicyExplicitMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []FamilyCatalog
		valid   bool
	}{
		{"single", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"nvke"}}}, true},
		{"distinct roles", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"nvke", "nvke-helm"}}, {Catalog: "other", Sources: []string{"nvke-cli"}}}, true},
		{"missing primary", []FamilyCatalog{{Catalog: "other", Sources: []string{"nvke"}}}, false},
		{"missing primary source", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"nvke-helm"}}}, false},
		{"repeated catalog", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"nvke"}}, {Catalog: "catalog", Sources: []string{"nvke-cli"}}}, false},
		{"repeated source", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"nvke", "nvke"}}}, false},
		{"escape", []FamilyCatalog{{Catalog: "../catalog", Sources: []string{"nvke"}}}, false},
		{"wildcard", []FamilyCatalog{{Catalog: "catalog", Sources: []string{"*"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (FamilyPolicy{Name: "nvke", Catalogs: tc.members}).validate("catalog", []string{"nvke"})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid family admitted")
			}
			if coded, ok := err.(*e.Error); !ok || coded.Code != e.CodeUsage {
				t.Fatalf("uncoded refusal: %v", err)
			}
		})
	}
}

func TestMaintainedFamilyPolicyBoundary(t *testing.T) {
	config, err := Load(t.Context(), "../../../inputs/policies/nvke-catalog-maintenance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if config.Family == nil || config.Family.Name != "nvke" || len(config.Family.Catalogs) != 4 {
		t.Fatal("maintained family boundary changed")
	}
	// Consolidated source identities include their diagnostic and local consumers.
	// Separately pinned local Helm and NKTP sources remain outside this family.
	selected := map[string]bool{}
	count := 0
	for _, member := range config.Family.Catalogs {
		for _, source := range member.Sources {
			selected[member.Catalog+":"+source] = true
			count++
		}
	}
	if count != 21 {
		t.Fatalf("source roles=%d, want 21", count)
	}
	for _, tc := range []struct {
		source   string
		included bool
	}{
		{"inputs/catalogs/nvke:nvke-profiles", true},
		{"inputs/catalogs/nvke:nvke-cli", true},
		{"inputs/catalogs/nvke:nvke-cli-local-site", true},
		{"inputs/catalogs/nvke:nvke-controller", true},
		{"inputs/catalogs/node-images:nvke-os", true},
		{"inputs/catalogs/nvke:nvke-controller-helm", false},
		{"inputs/catalogs/nvke:nvke-nktp-helm", false},
	} {
		if selected[tc.source] != tc.included {
			t.Errorf("family membership for %s = %v, want %v", tc.source, selected[tc.source], tc.included)
		}
	}
}
