// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootOriginalOutputRequiresExactInputsAndNamedSlot(t *testing.T) {
	fixture := newExactInputFixture(t)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.OpenPublication(t.Context(), fixture.layout, fixture.rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	description, err := publication.Description(fixture.reference)
	if err != nil {
		t.Fatal(err)
	}
	// Pure input-join unit test: the live byte owner separately checks that a
	// real output-local document's content subject matches its singleton output.
	name := description.Outputs[0].Name
	description.Outputs = description.Outputs[:1]
	for _, tc := range []struct {
		name string
		edit func(*delivery.PublicationDescription)
		code e.Code
	}{
		{name: "selected original output"},
		{name: "definition", edit: func(d *delivery.PublicationDescription) {
			d.Metadata.DefinitionDigest = "sha256:" + strings.Repeat("e", 64)
		}, code: e.CodeVerifyFailed},
		{name: "environment", edit: func(d *delivery.PublicationDescription) {
			d.Metadata.EnvironmentDigest = "sha256:" + strings.Repeat("e", 64)
		}, code: e.CodeVerifyFailed},
		{name: "Plan", edit: func(d *delivery.PublicationDescription) { d.Metadata.PlanDigest = "sha256:" + strings.Repeat("e", 64) }, code: e.CodeVerifyFailed},
		{name: "sibling", edit: func(d *delivery.PublicationDescription) { d.Outputs[0].Name = "sibling" }, code: e.CodeVerifyFailed},
		{name: "Build", edit: func(d *delivery.PublicationDescription) {
			d.Outputs[0].BuildDigest = "sha256:" + strings.Repeat("e", 64)
		}, code: e.CodeVerifyFailed},
		{name: "extra member", edit: func(d *delivery.PublicationDescription) { d.Outputs = append(d.Outputs, d.Outputs[0]) }, code: e.CodeVerifyFailed},
		{name: "aggregate origin", edit: func(d *delivery.PublicationDescription) {
			d.Outputs[0].Publication = &delivery.PublicationOrigin{Ref: fixture.reference}
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := description
			copy.Outputs = append([]delivery.PublicationOutput(nil), description.Outputs...)
			if tc.edit != nil {
				tc.edit(&copy)
			}
			err := requireBootOutputInputs(expectation, name, copy)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireBootPolicyCode(t, err, tc.code)
			}
		})
	}
}

func TestBootPreparationAdmissionPrecedesAdapters(t *testing.T) {
	fixture := newExactInputFixture(t)
	exact, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	expectation := &BootInputExpectation{exact: exact}
	for _, tc := range []struct {
		name        string
		policy      VerificationPolicy
		expectation *BootInputExpectation
		code        e.Code
	}{
		{"full publication policy", exactInputPolicy(), expectation, e.CodeEngineCapability},
		{"unissued expectation", selectedBootPolicyFixture(), nil, e.CodeGateUnmet},
		{"foreign destination", selectedBootPolicyFixture(), expectation, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			consumer := &Consumer{materialize: func(context.Context, string) (*delivery.Publication, error) { calls++; return nil, nil }}
			_, err := consumer.withPreparedBootAssets(t.Context(), tc.expectation, "foreign", nil, "", tc.policy,
				delivery.S3ObjectStoreOptions{}, delivery.BootReaderCredential{}, func(context.Context, *PreparedBootAssets) error { calls++; return nil })
			requireBootPolicyCode(t, err, tc.code)
			if calls != 0 {
				t.Fatal("rejected preparation reached an adapter")
			}
		})
	}
	_, err = PrepareBootInputs(fixture.plan)
	requireBootPolicyCode(t, err, e.CodeEngineCapability)
}
