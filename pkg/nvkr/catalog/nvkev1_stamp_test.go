// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"path/filepath"
	"reflect"
	"testing"
)

// nvkeV1CatalogDir is the shipped production catalog for the NVKE CLI. It is loaded here,
// rather than restated as a fixture, because the property under test is exactly that the
// document we ship stamps what upstream stamps.
const nvkeV1CatalogDir = "../../../inputs/catalogs/nvke/module-tags"

// The nvkectl binary this catalog builds must report the SAME version string as a binary
// built by NVKE's own cli/Makefile, including the leading v. The retained v1
// selection now follows the same repository release as the maintained M2 selection.
//
// This is a regression guard with a real failure behind it: the catalog originally
// rendered {{.Version}}, which also strips the v, so the published CLI reported
// version "0.1.0-rc.2" while the release it claimed to be reports "v0.1.0-rc.2". A digest
// chain cannot catch that; only comparing against upstream's own stamping rule can.
func TestNVKEV1CatalogStampsUpstreamModuleTag(t *testing.T) {
	cat, err := LoadContext(t.Context(), Dir(filepath.FromSlash(nvkeV1CatalogDir)))
	if err != nil {
		t.Fatalf("loading the shipped nvke-v1 catalog: %v", err)
	}
	component, ok := cat.Components["nvkectl"]
	if !ok {
		t.Fatalf("nvke-v1 catalog has no nvkectl component; components: %v", componentNames(cat))
	}

	// Derived from the lock (55612dce98cf0183790fbe5f4776225d63369fc2 @ 55612dce…, committer epoch 1792435542),
	// never from wall-clock time or the local checkout.
	want := map[string]string{
		"NVKECTL_VERSION": "55612dce98cf0183790fbe5f4776225d63369fc2",
		"NVKECTL_COMMIT":  "55612dce98cf0183790fbe5f4776225d63369fc2",
		"NVKECTL_DATE":    "2026-10-19T18:45:42Z",
	}
	if !reflect.DeepEqual(component.VersionStamp, want) {
		t.Errorf("nvkectl version stamps = %v, want %v", component.VersionStamp, want)
	}
}

func componentNames(cat *Catalog) []string {
	names := make([]string, 0, len(cat.Components))
	for name := range cat.Components {
		names = append(names, name)
	}
	return names
}
