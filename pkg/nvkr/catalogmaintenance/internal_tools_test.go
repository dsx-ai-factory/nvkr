// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"errors"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMaintenanceAdmitsInternalToolCatalogs(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		code       e.Code
	}{
		{"selected", "helper", ""},
		{"missing", "missing", e.CodeInvalidCatalog},
		{"absolute", "/helper", e.CodeUsage},
		{"escape", "../helper", e.CodeUsage},
		{"symlink", "link", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for _, name := range []string{"base", "helper"} {
				if err := os.Mkdir(name, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("helper", "link"); err != nil {
				t.Fatal(err)
			}
			def := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
				Metadata: definition.Metadata{Name: "test"}, Catalog: definition.Catalog{Base: "base"},
				Outputs: []definition.DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"cli"}}},
				InternalTools: []definition.InternalTool{{Name: "inspector", Platform: "linux/amd64", Format: definition.OutputExecutable,
					Components: []string{"helper"}, Result: "helper", Catalog: &definition.Catalog{Base: tc.base}}}}
			raw, err := canonical.Marshal(def)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("definition.json", raw, 0o600); err != nil {
				t.Fatal(err)
			}
			catalogs, err := definitionCatalogs(t.Context(), "definition.json")
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("got %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil || len(catalogs) != 2 || !catalogs["base"] || !catalogs["helper"] {
				t.Fatalf("catalogs=%v error=%v", catalogs, err)
			}
		})
	}
}
