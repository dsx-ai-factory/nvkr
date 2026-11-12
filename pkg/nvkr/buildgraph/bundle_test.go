// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBundleSelectionRequiresOneBuiltArchive(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*catalog.Catalog, *Selection)
		want   e.Code
	}{
		{name: "source archive"},
		{name: "raw producer", change: func(c *catalog.Catalog, _ *Selection) {
			v := c.Components["containerd"]
			v.OutputType = "raw"
			c.Components["containerd"] = v
		}, want: e.CodeInvalidCatalog},
		{name: "multiple producers", change: func(c *catalog.Catalog, s *Selection) {
			c.Components["copy"] = c.Components["containerd"]
			s.Components = append(s.Components, "copy")
		}, want: e.CodeInvalidCatalog},
		{name: "acquired archive", change: func(c *catalog.Catalog, _ *Selection) {
			c.Components["containerd"] = catalog.Component{Platforms: []string{"linux/amd64"}, OutputType: "tarball", Tag: "1.0.0",
				Prebuilt: &catalog.Prebuilt{URL: "https://example.test/bundle.tar.gz", Digests: map[string]string{"amd64": digAmd}, Evidence: &catalog.ExternalEvidence{None: true}}}
		}, want: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat := kubernetesFixture(t)
			c := cat.Components["containerd"]
			c.OutputType, c.ArtifactPath = "tarball", "bin/bundle.tar.gz"
			cat.Components["containerd"] = c
			selection := Selection{Components: []string{"containerd"}, Platform: "linux/amd64", OutputFormat: definition.OutputBundle}
			if test.change != nil {
				test.change(cat, &selection)
			}
			resolved, err := Resolve(cat, selection)
			if test.want != "" {
				if code(err) != test.want || resolved != nil {
					t.Fatalf("got %v, %v; want %s", resolved, err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Compile(resolved); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBundleLayoutSelectionRequiresExactClosure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
		mutate   func(*catalog.Catalog)
		want     e.Code
	}{
		{name: "explicit closure", selected: []string{"recipe", "bundle"}},
		{name: "missing material", selected: []string{"bundle"}, want: e.CodeInvalidCatalog},
		{name: "unrelated producer", selected: []string{"recipe", "bundle", "consumer"}, want: e.CodeInvalidCatalog},
		{name: "two roots", selected: []string{"recipe", "bundle", "second"}, mutate: func(c *catalog.Catalog) { c.Components["second"] = c.Components["bundle"] }, want: e.CodeInvalidCatalog},
		{name: "tool platform mismatch", selected: []string{"recipe", "bundle"}, mutate: func(c *catalog.Catalog) { c.Components["bundle"].ToolExecutable.Platform = "linux/amd64" }, want: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := pinnedToolCatalog()
			c := cat.Components["recipe"]
			c.OutputType, c.ArtifactPath = catalog.PinnedToolOutputOCILayout, ""
			c.Materials = map[string]string{"recipe.yaml": "recipe"}
			c.ToolArgs = []string{"bundle", "--recipe", `{{.Material "recipe.yaml"}}`, "--output", "oci-layout:{{.Output}}"}
			cat.Components["bundle"] = c
			if tc.mutate != nil {
				tc.mutate(cat)
			}
			resolved, err := Resolve(cat, Selection{Components: tc.selected, Platform: "linux/arm64", OutputFormat: definition.OutputBundle})
			if code(err) != tc.want {
				t.Fatalf("Resolve: %v, want %s", err, tc.want)
			}
			if err == nil {
				root, err := BundleRootName(resolved.Components)
				if err != nil || root != "bundle" {
					t.Fatalf("root %q: %v", root, err)
				}
				if _, err := Compile(resolved); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
