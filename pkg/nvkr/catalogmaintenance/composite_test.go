// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMaintenanceEnrollsCompositeBundles(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(name string, raw []byte) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{".github/nvkr-run-product", ".github/nvkr-dev-product"} {
		write(directory+"/manifest.json", []byte("{}"))
	}
	for _, tc := range []struct {
		name    string
		allowed map[string]bool
		refused bool
	}{
		{"both composite bundles enrolled", map[string]bool{".github/nvkr-run-product": true, ".github/nvkr-dev-product": true}, false},
		{"production composite omitted", map[string]bool{".github/nvkr-dev-product": true}, true},
		{"development composite omitted", map[string]bool{".github/nvkr-run-product": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseUnnamedBundles(tc.allowed)
			var coded *e.Error
			if tc.refused != (errors.As(err, &coded) && coded.Code == e.CodeEngineCapability) || (!tc.refused && err != nil) {
				t.Fatalf("refuseUnnamedBundles = %v", err)
			}
		})
	}

	// Maintenance admits a composite's rebased catalogs, not a plain-only load.
	member := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: "alpha"},
		Catalog: definition.Catalog{Base: "../../catalogs/alpha"}, Outputs: []definition.DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"cli"}}}}
	raw, err := canonical.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	write("inputs/families/alpha.yaml", raw)
	digest, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	composite := definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "product"},
		Members: []definition.CompositeMember{{Name: "alpha", Definition: "../families/alpha.yaml", Digest: digest}}}
	if raw, err = canonical.Marshal(composite); err != nil {
		t.Fatal(err)
	}
	write("inputs/products/composite.yaml", raw)
	if err := os.MkdirAll("catalogs/alpha", 0o755); err != nil {
		t.Fatal(err)
	}
	catalogs, err := definitionCatalogs(t.Context(), "inputs/products/composite.yaml")
	if err != nil || len(catalogs) != 1 || !catalogs["catalogs/alpha"] {
		t.Fatalf("composite maintenance catalogs = %v, %v", catalogs, err)
	}
}
