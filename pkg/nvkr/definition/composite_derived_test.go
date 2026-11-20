// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func compositeDerivedFixture(t *testing.T) (string, CompositeDefinition) {
	t.Helper()
	root, composite := compositeFixture(t)
	for i, name := range []string{"alpha", "beta"} {
		member := compositeMemberFixture(name, name+"-image")
		member.Outputs[0].Format = OutputOCI
		writeCompositeFile(t, filepath.Join(root, "defs", name+".yaml"), member)
		composite.Members[i] = memberPin(t, name, name+".yaml", member)
	}
	composite.DerivedOutputs = []CompositeDerivedOutput{{Name: "inventory", Platform: "linux/amd64",
		Derived: derivedoutput.Selection{Encoding: derivedoutput.InventoryV1, Outputs: []string{"alpha-image", "beta-image"}}}}
	return root, composite
}

func TestCompositeOwnedInventory(t *testing.T) {
	root, composite := compositeDerivedFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	composition, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	effective := composition.Effective()
	record, err := NewCompositionRecord(composition)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Validate(effective, composition.Digest()); err != nil {
		t.Fatal(err)
	}
	if len(effective.Outputs) != 3 || len(record.Members) != 2 || record.Outputs[2].Member != "" {
		t.Fatal("composite-derived output changed member attribution")
	}
	if _, ok := record.MemberOf("inventory"); ok {
		t.Fatal("composite-owned inventory became a family output")
	}
	if err := record.ValidateOutputLocal(composition.Digest(), []string{"inventory"}); err != nil {
		t.Fatal(err)
	}
	copy := composite.Clone()
	copy.DerivedOutputs[0].Derived.Outputs[0] = "changed"
	if composite.DerivedOutputs[0].Derived.Outputs[0] != "alpha-image" {
		t.Fatal("composite clone aliases derived materials")
	}
	cloned := record.Clone()
	cloned.DerivedOutputs[0].Derived.Outputs[0] = "changed"
	if record.DerivedOutputs[0].Derived.Outputs[0] != "alpha-image" {
		t.Fatal("record clone aliases derived materials")
	}
	legacy := composite.Clone()
	legacy.DerivedOutputs = nil
	legacyDigest, err := legacy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	want, err := CompositeDigest(legacy.Metadata, legacy.MemberIdentities(), legacy.Variants...)
	if err != nil || legacyDigest != want || legacyDigest == composition.Digest() {
		t.Fatal("derived declaration is not bound separately from legacy composite identity")
	}
}

func TestCompositeOwnedInventoryAdmission(t *testing.T) {
	declarations := func(count int) func(*CompositeDefinition) {
		return func(c *CompositeDefinition) {
			output := c.DerivedOutputs[0]
			c.DerivedOutputs = make([]CompositeDerivedOutput, count)
			for i := range c.DerivedOutputs {
				c.DerivedOutputs[i] = output
				c.DerivedOutputs[i].Name = fmt.Sprintf("inventory-%02d", i)
			}
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*CompositeDefinition)
		code e.Code
	}{
		{"member collision", func(c *CompositeDefinition) { c.DerivedOutputs[0].Name = "alpha-image" }, e.CodeInvalidCatalog},
		{"missing material", func(c *CompositeDefinition) { c.DerivedOutputs[0].Derived.Outputs = []string{"absent"} }, e.CodeInvalidCatalog},
		{"self material", func(c *CompositeDefinition) { c.DerivedOutputs[0].Derived.Outputs = []string{"inventory"} }, e.CodeInvalidCatalog},
		{"unknown encoding", func(c *CompositeDefinition) { c.DerivedOutputs[0].Derived.Encoding = "unknown" }, e.CodeInvalidCatalog},
		{"duplicate declaration", func(c *CompositeDefinition) { c.DerivedOutputs = append(c.DerivedOutputs, c.DerivedOutputs[0]) }, e.CodeInvalidCatalog},
		{"64 sorted declarations", declarations(64), ""},
		{"65 declarations", declarations(65), e.CodeInvalidCatalog},
		{"descending distinct declarations", func(c *CompositeDefinition) {
			declarations(2)(c)
			c.DerivedOutputs[0], c.DerivedOutputs[1] = c.DerivedOutputs[1], c.DerivedOutputs[0]
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, composite := compositeDerivedFixture(t)
			tc.edit(&composite)
			writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
			_, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, tc.code)
		})
	}
}

func TestCompositeOwnedInventoryRecordTampering(t *testing.T) {
	root, composite := compositeDerivedFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	composition, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewCompositionRecord(composition)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*CompositionRecord)
	}{
		{"removed declaration", func(r *CompositionRecord) { r.DerivedOutputs = nil }},
		{"changed material", func(r *CompositionRecord) { r.DerivedOutputs[0].Derived.Outputs = []string{"beta-image"} }},
		{"invented member", func(r *CompositionRecord) { r.Outputs[2].Member = "alpha" }},
		{"unowned payload", func(r *CompositionRecord) { r.Outputs[0].Member = "" }},
		{"removed attribution", func(r *CompositionRecord) { r.Outputs = r.Outputs[:2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := record.Clone()
			tc.edit(candidate)
			requireCode(t, candidate.ValidateOutputLocal(composition.Digest(), []string{"inventory"}), e.CodeUnverifiedRef)
		})
	}
	effective := composition.Effective()
	effective.Outputs[2].Derived.Outputs = []string{"alpha-image"}
	changed := record.Clone()
	changed.EffectiveDefinitionDigest, err = effective.Digest()
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, changed.Validate(effective, composition.Digest()), e.CodeUnverifiedRef)
}
