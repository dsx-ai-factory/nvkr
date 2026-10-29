// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func helmMemberFixture() Definition {
	definition := compositeMemberFixture("charts")
	definition.Outputs = []DefinitionOutput{
		{Name: "chart", Platform: "linux/amd64", Components: []string{"chart"}, Format: OutputHelm, Helm: &HelmOutput{Images: map[string]string{"controller": "controller-image"}}},
		{Name: "controller-image", Platform: "linux/amd64", Components: []string{"controller-image"}, Format: OutputOCI,
			Catalog: &Catalog{Base: "../images", Overlays: []string{"overlay.yaml"}}},
	}
	return definition
}

func mergeMembers() []AdmittedMember {
	return []AdmittedMember{
		{Name: "alpha", Path: "defs/alpha.yaml", Definition: compositeMemberFixture("alpha", "alpha-cli", "alpha-tool")},
		{Name: "beta", Path: "defs/beta.yaml", Definition: compositeMemberFixture("beta", "beta-cli")},
		{Name: "charts", Path: "defs/charts.yaml", Definition: helmMemberFixture()},
	}
}

func TestCompositeMergeContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		composite string
		mutate    func(members []AdmittedMember)
		code      e.Code
	}{
		{name: "valid beside members", composite: "defs/composite.yaml"},
		{name: "valid in another directory", composite: "product/composite.yaml"},
		{name: "duplicate identical output", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[1].Definition.Outputs = append(members[1].Definition.Outputs, members[0].Definition.Outputs[0])
		}},
		{name: "differing top-level base", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[1].Definition.Catalog.Base = "../release"
		}},
		{name: "differing top-level overlays", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[1].Definition.Catalog.Overlays = []string{"overlay.yaml"}
		}},
		{name: "same stack spelled from another directory", composite: "defs/composite.yaml", mutate: func(members []AdmittedMember) {
			members[1].Path = "defs/nested/beta.yaml"
			members[1].Definition.Catalog.Base = "../../catalog"
		}},
		{name: "differing sbomTool", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[1].Definition.SBOMTool = "trivy"
		}},
		{name: "omitted cosignTool", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[2].Definition.CosignTool = ""
		}},
		{name: "top-level catalog escapes root", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			for i := range members {
				members[i].Definition.Catalog.Base = "../../catalog"
			}
		}},
		{name: "output overlay escapes root", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[2].Definition.Outputs[1].Catalog.Overlays = []string{"../../overlay.yaml"}
		}},
		{name: "absolute catalog", composite: "defs/composite.yaml", code: e.CodeInvalidCatalog, mutate: func(members []AdmittedMember) {
			members[2].Definition.Outputs[1].Catalog.Base = "/catalog"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := mergeMembers()
			if tc.mutate != nil {
				tc.mutate(members)
			}
			effective, attribution, err := mergeComposite(tc.composite, Metadata{Name: "product"}, members)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := effective.Validate(); err != nil {
				t.Fatal(err)
			}
			wantBase, wantImages, wantOverlay := "../catalog", "../images", "overlay.yaml"
			if tc.composite == "product/composite.yaml" {
				wantOverlay = "../defs/overlay.yaml"
			}
			if effective.Metadata.Name != "product" || effective.Catalog.Base != wantBase || effective.SBOMTool != "syft" || len(effective.Outputs) != 5 {
				t.Fatalf("effective Definition = %+v", effective)
			}
			for _, output := range effective.Outputs {
				if output.Catalog == nil {
					t.Fatalf("output %q has no explicit catalog stack", output.Name)
				}
				switch output.Name {
				case "controller-image":
					if output.Catalog.Base != wantImages || len(output.Catalog.Overlays) != 1 || output.Catalog.Overlays[0] != wantOverlay {
						t.Fatalf("override stack not rebased: %+v", output.Catalog)
					}
				default:
					if output.Catalog.Base != wantBase {
						t.Fatalf("default stack not rebased for %q: %+v", output.Name, output.Catalog)
					}
				}
			}
			chart := effective.Outputs[3]
			if chart.Name != "chart" || chart.Helm.Images["controller"] != "controller-image" {
				t.Fatalf("Helm binding changed: %+v", chart)
			}
			want := []OutputAttribution{{"alpha-cli", "alpha"}, {"alpha-tool", "alpha"}, {"beta-cli", "beta"}, {"chart", "charts"}, {"controller-image", "charts"}}
			if len(attribution) != len(want) {
				t.Fatalf("attribution = %+v", attribution)
			}
			for i := range want {
				if attribution[i] != want[i] {
					t.Fatalf("attribution = %+v", attribution)
				}
			}
		})
	}
}

func TestCompositeMergeIsDeterministic(t *testing.T) {
	members := mergeMembers()
	first, firstAttribution, err := mergeComposite("defs/composite.yaml", Metadata{Name: "product"}, members)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := []AdmittedMember{members[2], members[0], members[1]}
	second, secondAttribution, err := mergeComposite("defs/composite.yaml", Metadata{Name: "product"}, shuffled)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := canonical.Marshal(struct {
		D Definition
		A []OutputAttribution
	}{first, firstAttribution})
	right, _ := canonical.Marshal(struct {
		D Definition
		A []OutputAttribution
	}{second, secondAttribution})
	if !bytes.Equal(left, right) {
		t.Fatal("member order changed the effective Definition")
	}
	// The merge must not alias or mutate admitted member values.
	if members[2].Definition.Outputs[0].Catalog != nil || members[2].Definition.Outputs[1].Catalog.Base != "../images" {
		t.Fatal("merge mutated an admitted member")
	}
}

func TestCompositeAdmissionDerivesEffectiveDefinition(t *testing.T) {
	root, composite := compositeFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	admitted, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	effective := admitted.Effective()
	if effective.Metadata.Name != "product" || len(effective.Outputs) != 2 || effective.Outputs[0].Catalog.Base != "../catalog" {
		t.Fatalf("effective Definition = %+v", effective)
	}
	if _, err := effective.Digest(); err != nil {
		t.Fatal(err)
	}
	effective.Outputs[0].Name = "changed"
	if admitted.Effective().Outputs[0].Name != "alpha-cli" || admitted.Attribution()[1].Member != "beta" {
		t.Fatal("effective Definition is not independently owned")
	}
}
