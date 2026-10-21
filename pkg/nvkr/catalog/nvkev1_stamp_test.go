// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"path/filepath"
	"testing"
)

// nvkeV1CatalogDir is the shipped production catalog for the NVKE CLI. It is loaded here,
// rather than restated as a fixture, because the property under test is exactly that the
// document we ship stamps what upstream stamps.
const nvkeV1CatalogDir = "../../../inputs/catalogs/nvke/module-tags"

// The nvkectl binary this catalog builds must report the SAME version string as a binary
// built by NVKE's own cli/Makefile, which strips the `cli/` module prefix from the tag and
// stamps the remainder verbatim — leading v included.
//
// This is a regression guard with a real failure behind it: the catalog originally
// rendered {{.Version}}, which also strips the v, so the published CLI reported
// version "0.1.0-rc.2" while the release it claimed to be reports "v0.1.0-rc.2". A digest
// chain cannot catch that; only comparing against upstream's own stamping rule can.
func TestNVKEV1CatalogStampsUpstreamModuleTag(t *testing.T) {
	cat, err := LoadContext(context.Background(), Dir(filepath.FromSlash(nvkeV1CatalogDir)))
	if err != nil {
		t.Fatalf("loading the shipped nvke-v1 catalog: %v", err)
	}
	component, ok := cat.Components["nvkectl"]
	if !ok {
		t.Fatalf("nvke-v1 catalog has no nvkectl component; components: %v", componentNames(cat))
	}

	// Derived from the v0.22.0 lock and its committer epoch,
	// never from wall-clock time or the local checkout.
	want := map[string]string{
		"NVKECTL_VERSION": "v0.22.0",
		"NVKECTL_COMMIT":  "400c24fb",
		"NVKECTL_DATE":    "2026-10-21T02:06:38Z",
	}
	for key, value := range want {
		if got := component.VersionStamp[key]; got != value {
			t.Errorf("nvkectl versionStamp[%s] = %q, want %q", key, got, value)
		}
	}
}

func componentNames(cat *Catalog) []string {
	names := make([]string, 0, len(cat.Components))
	for name := range cat.Components {
		names = append(names, name)
	}
	return names
}
