// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

// Materialize authored fixture inputs so the mixed-output test uses real
// compiler capture and replay. Pins describe fixture sources, not fetched builds.
func captureMixedFixtureInputs(t *testing.T, definition Definition, environment Environment, base *catalog.Catalog) *PreparedRun {
	t.Helper()
	root := t.TempDir()
	components := map[string]map[string]any{}
	sources := map[string]catalog.Source{}
	pins := map[string]catalog.Pin{}
	for name, component := range base.Components {
		raw, err := canonical.Marshal(component)
		if err != nil {
			t.Fatal(err)
		}
		var authored map[string]any
		if err := canonical.UnmarshalExact(raw, &authored); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"repo", "tag", "commit", "treeHash", "toolchainImage", "toolchainImages", "sourceDateEpoch", "vendorCommand"} {
			delete(authored, field)
		}
		authored["source"] = name
		components[name] = authored
		sources[name] = catalog.Source{Repo: component.Repo, ToolchainImage: component.ToolchainImage, ToolchainImagesByArch: component.ToolchainImages, VendorCommand: component.VendorCommand}
		pins[name] = catalog.Pin{Tag: component.Tag, Commit: component.Commit, TreeHash: component.TreeHash, SourceDateEpoch: component.SourceDate}
	}
	writeCanonicalTestFile(t, filepath.Join(root, "catalog", "registry.yaml"), map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "sources": sources, "components": components})
	writeCanonicalTestFile(t, filepath.Join(root, "catalog", "pins.lock.yaml"), catalog.PinLock{APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: pins})
	writeCanonicalTestFile(t, filepath.Join(root, "definition.json"), definition)
	writeCanonicalTestFile(t, filepath.Join(root, "environment.json"), environment)
	prepared, err := PrepareCapturedFiles(t.Context(), root, "definition.json", "environment.json", "test", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
