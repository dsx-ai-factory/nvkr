// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDeclarativeHelmAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Definition)
	}{
		{name: "current NVKE chart"},
		{name: "missing output", change: func(d *Definition) {
			d.Outputs[0].Helm.Images["controller"] = "missing"
		}},
		{name: "wrong platform", change: func(d *Definition) { d.Outputs[1].Platform = "linux/arm64" }},
		{name: "wrong format", change: func(d *Definition) { d.Outputs[1].Format = OutputExecutable }},
		{name: "empty slots", change: func(d *Definition) { d.Outputs[0].Helm.Images = nil }},
		{name: "invalid slot", change: func(d *Definition) { d.Outputs[0].Helm.Images = map[string]string{"bad slot": "cwe"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := LoadDefinition(t.Context(), filepath.Join("..", "..", "..", "docs", "examples", "nvke-controller-bindings", "definition.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&d)
			}
			err = d.Validate()
			if tc.change == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("expected invalid catalog, got %v", err)
			}
		})
	}
}

func TestHelmRelationshipsClone(t *testing.T) {
	original := Definition{Outputs: []DefinitionOutput{{Helm: &HelmOutput{Images: map[string]string{"controller": "cwe"}}}}}
	cloned := original.Clone()
	cloned.Outputs[0].Helm.Images["controller"] = "other"
	if original.Outputs[0].Helm.Images["controller"] != "cwe" {
		t.Fatal("cloned relationships alias original")
	}
}
