// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"path/filepath"
	"testing"
)

// nkeV1CatalogDir is the shipped production catalog for the NKE CLI. It is loaded here,
// rather than restated as a fixture, because the property under test is exactly that the
// document we ship stamps what upstream stamps.
const nkeV1CatalogDir = "../../../definitions/nke-v1/catalog"

// The nkectl binary this catalog builds must report the SAME version string as a binary
// built by NKE's own cli/Makefile, which strips the `cli/` module prefix from the tag and
// stamps the remainder verbatim — leading v included.
//
// This is a regression guard with a real failure behind it: the catalog originally
// rendered {{.Version}}, which also strips the v, so the published nkectl reported
// "nkectl 0.1.0-rc.2" while the release it claimed to be reports "v0.1.0-rc.2". A digest
// chain cannot catch that; only comparing against upstream's own stamping rule can.
func TestNKEV1CatalogStampsUpstreamModuleTag(t *testing.T) {
	cat, err := LoadContext(context.Background(), Dir(filepath.FromSlash(nkeV1CatalogDir)))
	if err != nil {
		t.Fatalf("loading the shipped nke-v1 catalog: %v", err)
	}
	component, ok := cat.Components["nkectl"]
	if !ok {
		t.Fatalf("nke-v1 catalog has no nkectl component; components: %v", componentNames(cat))
	}

	// Derived from the lock (cli/v0.1.0-rc.2 @ ed9fb09b…, committer epoch 1787066575),
	// never from wall-clock time or the local checkout.
	want := map[string]string{
		"NKECTL_VERSION":      "v0.1.0-rc.2",
		"NKECTL_COMMIT":       "ed9fb09b",
		"NKECTL_DATE":         "2026-08-18T15:22:55Z",
		"NKECTL_CAPI_VERSION": "v1.12.3",
	}
	for key, value := range want {
		if got := component.VersionStamp[key]; got != value {
			t.Errorf("nkectl versionStamp[%s] = %q, want %q", key, got, value)
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
