// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func internalToolDefinition() Definition {
	return Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind,
		Metadata: Metadata{Name: "fixture"}, Catalog: Catalog{Base: "catalog"},
		Outputs:       []DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Components: []string{"cli"}, Format: OutputExecutable}},
		InternalTools: []InternalTool{{Name: "inspector", Platform: "linux/amd64", Components: []string{"helper"}, Result: "helper", Format: OutputExecutable}},
	}
}

func TestInternalToolSelectionAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Definition)
		code   e.Code
	}{
		{"executable", func(*Definition) {}, ""},
		{"runtime image", func(d *Definition) { d.InternalTools[0].Format = OutputOCI }, ""},
		{"arm64", func(d *Definition) { d.InternalTools[0].Platform = "linux/arm64" }, ""},
		{"empty name", func(d *Definition) { d.InternalTools[0].Name = "" }, e.CodeInvalidCatalog},
		{"invalid scanner", func(d *Definition) { d.InternalTools[0].SBOMTool = "../scanner" }, e.CodeInvalidCatalog},
		{"duplicate", func(d *Definition) { d.InternalTools = append(d.InternalTools, d.InternalTools[0]) }, e.CodeInvalidCatalog},
		{"unordered", func(d *Definition) { d.InternalTools = append(d.InternalTools, InternalTool{Name: "a"}) }, e.CodeInvalidCatalog},
		{"unknown platform", func(d *Definition) { d.InternalTools[0].Platform = "linux/386" }, e.CodeEngineCapability},
		{"unknown form", func(d *Definition) { d.InternalTools[0].Format = "directory" }, e.CodeEngineCapability},
		{"empty override", func(d *Definition) { d.InternalTools[0].Catalog = &Catalog{} }, e.CodeInvalidCatalog},
		{"unselected result", func(d *Definition) { d.InternalTools[0].Result = "other" }, e.CodeInvalidCatalog},
		{"empty producers", func(d *Definition) { d.InternalTools[0].Components = nil }, e.CodeInvalidCatalog},
		{"repeated producer", func(d *Definition) { d.InternalTools[0].Components = []string{"helper", "helper"} }, e.CodeInvalidCatalog},
		{"invalid producer", func(d *Definition) { d.InternalTools[0].Components = []string{"../helper", "helper"} }, e.CodeInvalidCatalog},
		{"ambiguous image", func(d *Definition) {
			d.InternalTools[0].Format = OutputOCI
			d.InternalTools[0].Components = []string{"helper", "second"}
		}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := internalToolDefinition()
			test.change(&input)
			err := input.Validate()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want %s", err, test.code)
			}
		})
	}
}

func TestInternalToolCloneAndDefinitionIdentity(t *testing.T) {
	input := internalToolDefinition()
	input.InternalTools[0].Catalog = &Catalog{Base: "tools", Overlays: []string{"tool-overlay.yaml"}}
	original, err := input.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*InternalTool)
	}{
		{"producer", func(tool *InternalTool) { tool.Components[0] = "replacement"; tool.Result = "replacement" }},
		{"platform", func(tool *InternalTool) { tool.Platform = "linux/arm64" }},
		{"scanner", func(tool *InternalTool) { tool.SBOMTool = "native-scanner" }},
		{"catalog", func(tool *InternalTool) { tool.Catalog.Base = "other" }},
		{"overlay", func(tool *InternalTool) { tool.Catalog.Overlays[0] = "other.yaml" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			clone := input.Clone()
			test.change(&clone.InternalTools[0])
			changed, err := clone.Digest()
			if err != nil || changed == original {
				t.Fatalf("tool selection not bound: %v", err)
			}
			stillOriginal, err := input.Digest()
			if err != nil || stillOriginal != original {
				t.Fatalf("clone mutated caller: %v", err)
			}
			if !reflect.DeepEqual(clone.Outputs, input.Outputs) {
				t.Fatal("internal tool changed delivery output selection")
			}
		})
	}
}
