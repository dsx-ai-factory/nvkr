// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"encoding/json"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestComparisonViewModes(t *testing.T) {
	before := inspectPlan(t, maintainedPlan(t))
	changed := maintainedPlan(t)
	changed.GeneratorVersion = "next-generator"
	after := inspectPlan(t, changed)
	comparison, err := Compare(before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		options  ComparisonOptions
		contains string
	}{
		{"summary", ComparisonOptions{Format: "text"}, "Direct inputs and routes"},
		{"details", ComparisonOptions{Format: "text", Details: true}, "long values abbreviated"},
		{"json", ComparisonOptions{Format: "json"}, `"schemaVersion":"nvkr.dev/plan-comparison/v1"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := comparison.RenderView(test.options)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), test.contains) || !strings.Contains(string(raw), "generatorVersion") {
				t.Fatalf("missing requested view: %s", raw)
			}
			if test.options.Format == "json" {
				var result Comparison
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Changes) != 1 {
					t.Fatalf("changes = %d", len(result.Changes))
				}
			}
		})
	}
}

func TestComparisonViewRejectsIneffectiveOptions(t *testing.T) {
	for _, options := range []ComparisonOptions{
		{Format: "json", Details: true}, {Format: "yaml"}, {},
	} {
		t.Run(options.Format, func(t *testing.T) {
			assertCode(t, options.Validate(), e.CodeUsage)
			var comparison *Comparison
			raw, err := comparison.RenderView(options)
			assertCode(t, err, e.CodeUsage)
			if len(raw) != 0 {
				t.Fatalf("rejected view emitted %q", raw)
			}
		})
	}
}
