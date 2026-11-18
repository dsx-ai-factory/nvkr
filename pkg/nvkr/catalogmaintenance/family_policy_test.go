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
	if config.Family == nil || config.Family.Name != "nvke" || len(config.Family.Catalogs) != 11 {
		t.Fatal("maintained family boundary changed")
	}
	excluded := map[string]bool{"inputs/catalogs/controller-profiles/nvidia-arm64": true, "inputs/catalogs/node-images/diagnostic": true, "inputs/catalogs/nvke/local-site-arm64": true, "inputs/catalogs/nvke/module-cli-arm64": true}
	count := 0
	for _, member := range config.Family.Catalogs {
		if excluded[member.Catalog] {
			t.Fatalf("independent cohort enrolled: %s", member.Catalog)
		}
		count += len(member.Sources)
	}
	if count != 25 {
		t.Fatalf("source roles=%d, want 25", count)
	}
}
