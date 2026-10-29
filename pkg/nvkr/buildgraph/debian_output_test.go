// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func debianOutputFixture() *catalog.Catalog {
	return &catalog.Catalog{Components: map[string]catalog.Component{
		"package": {
			Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
			Repo: "https://example.test/package", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
			ToolchainImages: map[string]string{"amd64": "sha256:" + strings.Repeat("c", 64), "arm64": "sha256:" + strings.Repeat("d", 64)},
			BuildCommand:    []string{"make", "package"}, ArtifactPath: "out/package.deb", OutputType: "host-package", SourceDate: 1, WriteDir: "out",
		},
	}}
}

func TestDebianOutputUsesOrdinaryHostPackageGraph(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			cat := debianOutputFixture()
			selection := Selection{Components: []string{"package"}, Platform: platform, OutputFormat: definition.OutputDebianPackage}
			resolved, err := Resolve(cat, selection)
			if err != nil {
				t.Fatal(err)
			}
			raw := mustCompile(t, resolved)
			selection.OutputFormat = ""
			ordinary, err := Resolve(cat, selection)
			if err != nil {
				t.Fatal(err)
			}
			want, err := mustCompile(t, ordinary).Digest()
			if err != nil {
				t.Fatal(err)
			}
			got, err := raw.Digest()
			if err != nil || got != want {
				t.Fatalf("raw format changed ordinary producer identity: %s != %s, %v", got, want, err)
			}
			consumer := cat.Components["package"]
			consumer.Materials = map[string]string{"package": "package"}
			consumer.ArtifactPath = "out/materials.tar"
			consumer.OutputType = "tarball"
			cat.Components["consumer"] = consumer
			selection.Components = []string{"package", "consumer"}
			material, err := Resolve(cat, selection)
			if err != nil {
				t.Fatal(err)
			}
			var bindings int
			for _, node := range mustCompile(t, material).Nodes {
				if node.Kind == ir.KindBuild && node.Component == "consumer" {
					bindings = len(node.Materials)
					if bindings != 1 || node.Materials[0].Component != "package" {
						t.Fatalf("ordinary consumer lost package material: %#v", node.Materials)
					}
				}
			}
			if bindings != 1 {
				t.Fatal("ordinary consumer build is missing")
			}
		})
	}
}

func TestDebianOutputAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*catalog.Catalog, *Selection)
		want e.Code
	}{
		{"missing platform", func(_ *catalog.Catalog, s *Selection) { s.Platform = "" }, e.CodeInvalidCatalog},
		{"foreign platform", func(_ *catalog.Catalog, s *Selection) { s.Platform = "darwin/arm64" }, e.CodeInvalidCatalog},
		{"no producer", func(_ *catalog.Catalog, s *Selection) { s.Components = nil }, e.CodeInvalidCatalog},
		{"multiple producers", func(_ *catalog.Catalog, s *Selection) { s.Components = []string{"package", "other"} }, e.CodeInvalidCatalog},
		{"other output", func(c *catalog.Catalog, _ *Selection) {
			p := c.Components["package"]
			p.OutputType = "raw"
			c.Components["package"] = p
		}, e.CodeInvalidCatalog},
		{"excluded producer", func(c *catalog.Catalog, _ *Selection) {
			c.Components["package"] = catalog.Component{Platforms: []string{"linux/amd64"}, ExclusionReason: "not distributed"}
		}, e.CodeInvalidCatalog},
		{"missing arch toolchain", func(c *catalog.Catalog, _ *Selection) { delete(c.Components["package"].ToolchainImages, "amd64") }, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := debianOutputFixture()
			selection := Selection{Components: []string{"package"}, Platform: "linux/amd64", OutputFormat: definition.OutputDebianPackage}
			tc.edit(cat, &selection)
			resolved, err := Resolve(cat, selection)
			if resolved != nil || code(err) != tc.want {
				t.Fatalf("Resolve() = %#v, %v; want no result and %s", resolved, err, tc.want)
			}
		})
	}
}
