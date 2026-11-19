// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"fmt"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCatalogListAdmissionAndClone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		catalogs []Catalog
		invalid  bool
	}{
		{"omitted", nil, false},
		{"one", []Catalog{{Base: "../catalog", Overlays: []string{"overlay.yaml"}}}, false},
		{"empty base", []Catalog{{}}, true},
		{"repeated", []Catalog{{Base: "../catalog"}, {Base: "../catalog"}}, true},
		{"too many", func() []Catalog {
			var all []Catalog
			for i := 0; i < 65; i++ {
				all = append(all, Catalog{Base: fmt.Sprintf("catalog-%d", i)})
			}
			return all
		}(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := compositeMemberFixture("alpha", "cli")
			def.Catalogs = tc.catalogs
			err := def.Validate()
			if tc.invalid {
				requireCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copy := def.Clone()
			if len(copy.Catalogs) > 0 {
				copy.Catalogs[0].Base = "changed"
				copy.Catalogs[0].Overlays[0] = "changed"
				if def.Catalogs[0].Base == "changed" || def.Catalogs[0].Overlays[0] == "changed" {
					t.Fatal("clone aliases candidates")
				}
			}
		})
	}
}

func TestCompositeCatalogLists(t *testing.T) {
	for _, mode := range []string{"same", "reordered", "omitted", "rebased"} {
		different := mode == "reordered" || mode == "omitted"
		t.Run(mode, func(t *testing.T) {
			members := mergeMembers()
			for i := range members {
				members[i].Definition.Catalogs = []Catalog{{Base: "../catalog"}, {Base: "../images"}}
			}
			if mode == "reordered" {
				members[1].Definition.Catalogs[0], members[1].Definition.Catalogs[1] = members[1].Definition.Catalogs[1], members[1].Definition.Catalogs[0]
			}
			if mode == "omitted" {
				members[1].Definition.Catalogs = nil
			}
			if mode == "rebased" {
				members[1].Path = "defs/nested/beta.yaml"
				members[1].Definition.Catalog.Base = "../../catalog"
				members[1].Definition.Catalogs = []Catalog{{Base: "../../catalog"}, {Base: "../../images"}}
			}
			effective, _, err := mergeComposite("product/composite.yaml", Metadata{Name: "product"}, members)
			if different {
				requireCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(effective.Catalogs) != 2 || effective.Outputs[0].Catalog != nil {
				t.Fatal("composite discarded list selection")
			}
			found := false
			for _, output := range effective.Outputs {
				if output.Name != "controller-image" {
					continue
				}
				found = true
				if output.Catalog == nil || output.Catalog.Base != "../images" || len(output.Catalog.Overlays) != 1 || output.Catalog.Overlays[0] != "../defs/overlay.yaml" {
					t.Fatalf("list mode lost rebased explicit override: %+v", output.Catalog)
				}
			}
			if !found {
				t.Fatal("explicit output omitted")
			}
		})
	}
}
