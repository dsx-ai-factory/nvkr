// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDefinitionRejectsRemovedAuthoringModel(t *testing.T) {
	const direct = `apiVersion: nkd.dev/definition/v4
kind: Definition
metadata:
  name: example
catalog:
  base: catalog
outputs:
  - name: cli
    components: [kubectl]
    platform: linux/amd64
    format: executable
`
	validPath := filepath.Join(t.TempDir(), "definition.yaml")
	if err := os.WriteFile(validPath, []byte(direct), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDefinition(t.Context(), validPath); err != nil {
		t.Fatalf("current authoring model must be admitted: %v", err)
	}
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "prior major", raw: strings.Replace(direct, "definition/v4", "definition/v3", 1)},
		{name: "authored recipes", raw: direct + "recipes: []\n"},
		{name: "output recipe reference", raw: direct + "    recipe: worker\n"},
		{name: "criteria matcher", raw: direct + "    criteria: {arch: amd64}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "definition.yaml")
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadDefinition(t.Context(), path)
			assertCode(t, err, e.CodeInvalidCatalog)
		})
	}
}
