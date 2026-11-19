// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestRetainedCombinedQualificationOutputMembership(t *testing.T) {
	def, err := definition.LoadDefinition(t.Context(), "../../../inputs/definitions/nvke/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	outputs := make(map[string]definition.DefinitionOutput, len(def.Outputs))
	for _, output := range def.Outputs {
		outputs[output.Name] = output
	}
	for _, tc := range []struct{ name, path string }{
		{"production", "../../../inputs/policies/nvke-qualification.yaml"},
		{"development", "../../../inputs/policies/nvke-development-qualification.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := lifecycle.LoadQualificationPolicy(t.Context(), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if policy.Smoke == nil || len(policy.Smoke.NonToolOutputs) == 0 {
				t.Fatal("retained policy has no non-tool output rules")
			}
			for _, rule := range policy.Smoke.NonToolOutputs {
				t.Run(rule.Output, func(t *testing.T) {
					output, ok := outputs[rule.Output]
					if !ok {
						t.Fatalf("policy output %q is absent from the retained combined Definition", rule.Output)
					}
					if rule.Kind != output.Format {
						t.Errorf("policy kind = %q, Definition format = %q", rule.Kind, output.Format)
					}
					got, want := slices.Clone(rule.Components), slices.Clone(output.Components)
					slices.Sort(got)
					slices.Sort(want)
					if !slices.Equal(got, want) {
						t.Errorf("policy components = %v, Definition components = %v", got, want)
					}
				})
			}
		})
	}
}
