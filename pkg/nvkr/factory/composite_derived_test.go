// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
)

func TestCompositeDerivedInventoryPlanStorage(t *testing.T) {
	definition, environment, stack := derivedTestInputs(t)
	definition.Outputs = definition.Outputs[:1]
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind,
		Metadata: authored.Metadata{Name: "release"}, DerivedOutputs: []authored.CompositeDerivedOutput{{Name: "inventory", Platform: "linux/amd64",
			Derived: derivedoutput.Selection{Encoding: derivedoutput.InventoryV1, Outputs: []string{"alpha-chart", "beta-chart"}}}}}
	files := map[string][]byte{}
	for _, name := range []string{"alpha", "beta"} {
		member := definition.Clone()
		member.Metadata.Name = name
		member.Outputs[0].Name = name + "-chart"
		digest, err := member.Digest()
		if err != nil {
			t.Fatal(err)
		}
		files[name+".json"] = mustCanonical(t, member)
		composite.Members = append(composite.Members, authored.CompositeMember{Name: name, Definition: name + ".json", Digest: digest})
	}
	composition, err := authored.AdmitComposite(t.Context(), "composite.json", mustCanonical(t, composite), func(_ context.Context, path string) ([]byte, error) {
		return files[path], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	load := func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil }
	plan, err := compileComposition(t.Context(), composition, "environment.json", environment, "test", load)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 2 || len(plan.LifecycleOutputs) != 3 || plan.Composition == nil {
		t.Fatal("composite inventory changed native membership or lost attribution")
	}
	if _, owned := plan.Composition.MemberOf("inventory"); owned {
		t.Fatal("inventory is attributed to a payload member")
	}
	const planPath = ".github/nvkr-run/plan.json"
	tree, err := renderPlanTree(t.Context(), plan, planPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, raw, err := joinPlanTree(t.Context(), planPath, tree)
	if err != nil || !bytes.Equal(raw, mustCanonical(t, plan)) {
		t.Fatalf("shared Plan records changed composite inventory: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	environment.Publication.RepositoryPrefix = "localhost:5000/relocated"
	relocated, err := compileComposition(t.Context(), composition, "environment.json", environment, "test", load)
	if err != nil {
		t.Fatal(err)
	}
	for i, output := range plan.LifecycleOutputs {
		changed := output.BuildDigest != relocated.LifecycleOutputs[i].BuildDigest
		if changed != (output.Name == "inventory") {
			t.Fatalf("relocation changed the wrong identity: %s", output.Name)
		}
	}
}
