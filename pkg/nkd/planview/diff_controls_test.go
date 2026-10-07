// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestComparisonShortValuesEscapeControls(t *testing.T) {
	for _, test := range []struct{ name, value, escaped string }{
		{"bidi override", "\u202e", `\u202e`},
		{"C1 control", "\u009b", `\u009b`},
		{"DEL", "\x7f", `\u007f`},
		{"bidi isolate", "\u2066", `\u2066`},
		{"supplementary control", "\U000e0001", `\udb40\udc01`},
		{"printable Unicode and JSON escapes", "東🙂\"\\", `東🙂\"\\`},
	} {
		t.Run(test.name, func(t *testing.T) {
			withValue := func(value string) *Document {
				return inspectPlan(t, comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
					env.Runtime.Variables = map[string]string{"message": value}
				}))
			}
			before, after := withValue("before"+test.value), withValue("after"+test.value)
			comparison, err := Compare(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(comparison.Builds) != 0 || comparison.UnchangedBuilds != len(before.plan.Builds) {
				t.Fatal("runtime value changed Build identities")
			}
			for _, details := range []bool{false, true} {
				output, err := comparison.RenderView(ComparisonOptions{Format: "text", Details: details})
				if err != nil {
					t.Fatal(err)
				}
				want := `"before` + test.escaped + `" -> "after` + test.escaped + `"`
				if !strings.Contains(string(output), want) {
					t.Errorf("details=%t: missing escaped values %s in %q", details, want, output)
				}
				for _, r := range test.value {
					if !strconv.IsPrint(r) && strings.ContainsRune(string(output), r) {
						t.Errorf("details=%t: raw display control %U", details, r)
					}
				}
			}
			raw, err := comparison.RenderView(ComparisonOptions{Format: "json"})
			if err != nil {
				t.Fatal(err)
			}
			var restored Comparison
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(comparison, &restored) {
				t.Fatal("comparison JSON changed exact values")
			}
			for _, doc := range []*Document{before, after} {
				raw, err := doc.Render(PlanOptions{Format: "json"})
				if err != nil {
					t.Fatal(err)
				}
				var restored factory.Plan
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, doc.raw) || restored.Environment.Runtime.Variables["message"] != doc.plan.Environment.Runtime.Variables["message"] {
					t.Fatal("Plan JSON changed exact bytes or runtime value")
				}
			}
		})
	}
}
