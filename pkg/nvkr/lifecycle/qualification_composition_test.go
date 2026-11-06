// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"regexp"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func qualificationCompositePlan(t *testing.T, plan *factory.Plan) {
	t.Helper()
	effective, err := plan.Definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	record := &definition.CompositionRecord{Kind: definition.CompositeKind, Metadata: plan.Definition.Metadata,
		EffectiveDefinitionDigest: effective, Members: []definition.MemberIdentity{{Name: "family", Digest: effective}}}
	for _, output := range plan.LifecycleOutputs {
		record.Outputs = append(record.Outputs, definition.OutputAttribution{Output: output.Name, Member: "family"})
	}
	plan.DefinitionDigest, err = definition.CompositeDigest(record.Metadata, record.Members)
	if err != nil {
		t.Fatal(err)
	}
	plan.Composition, plan.SchemaVersion = record, factory.CompositePlanSchemaVersion
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestQualificationAuthenticatesCompleteComposition(t *testing.T) {
	for _, mode := range []string{"complete", "changed attribution", "missing output", "changed member"} {
		t.Run(mode, func(t *testing.T) {
			request, consumer, verifier := newQualificationFixtureWithSigningIdentity(t,
				"^"+regexp.QuoteMeta(qualificationSourceFixture().value.BuilderID)+"$",
				qualificationFixtureOptions{plan: func(plan *factory.Plan) { qualificationCompositePlan(t, plan) }})
			switch mode {
			case "changed attribution":
				request.Plan.Composition.Outputs[0].Member = "unselected"
			case "missing output":
				request.Plan.Composition.Outputs = request.Plan.Composition.Outputs[1:]
			case "changed member":
				request.Plan.Composition.Members[0].Name = "other"
			}
			receipt, report, err := consumer.checkQualification(t.Context(), request, verifier.source)
			if mode != "complete" {
				requireLifecycleCode(t, err, e.CodeUnverifiedRef)
				if receipt != nil {
					t.Fatal("invalid composition issued receipt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = receipt.Close() }()
			if !report.Composition.Equal(request.Plan.Composition) {
				t.Fatal("checks lost authenticated composition")
			}
			report.Composition.Members[0].Name = "mutated"
			frozen, err := receipt.Description()
			if err != nil || !frozen.Composition.Equal(request.Plan.Composition) {
				t.Fatalf("mutable report changed receipt: %v", err)
			}
		})
	}
}
