// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianSourceFixture(t *testing.T, selected target.Debian) (*buildgraph.Resolved, string, string) {
	t.Helper()
	routes := []catalog.DebianRoute{}
	for _, distribution := range []string{"debian-13", "ubuntu-24.04"} {
		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			pin := strings.Repeat(string(rune('a'+len(routes))), 64)
			routes = append(routes, catalog.DebianRoute{
				Target:         target.Debian{Distribution: distribution, Platform: platform},
				ToolchainImage: "example.test/build@sha256:" + pin,
				BuildCommand:   []string{"make", "deb"}, ArtifactPath: "out/package.deb", WriteDir: "out",
			})
		}
	}
	cat := &catalog.Catalog{Components: map[string]catalog.Component{"agent": {
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		Repo: "https://example.test/source", Tag: "v9.1.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		SourceDate: 1, OutputType: catalog.OutputDebianPackage, BuilderType: catalog.BuilderOCIHermetic,
		Debian: &catalog.DebianSpec{Routes: routes},
	}}}
	res, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{"agent"}, OutputFormat: catalog.OutputDebianPackage, Debian: &selected})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(res)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return res, digest, graph.Nodes[0].Toolchain
}

func TestDebianSourceDocumentUsesExactCompiledRoute(t *testing.T) {
	for _, distribution := range []string{"debian-13", "ubuntu-24.04"} {
		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			t.Run(distribution+"/"+platform, func(t *testing.T) {
				selected := target.Debian{Distribution: distribution, Platform: platform}
				res, digest, toolchain := debianSourceFixture(t, selected)
				hash := "sha256:" + strings.Repeat("f", 64)
				boms, index, err := PerArtifact(res, digest, map[string]string{"agent": hash})
				if err != nil {
					t.Fatal(err)
				}
				props := map[string]string{}
				for _, property := range boms["agent"].Metadata.Component.Properties {
					props[property.Name] = property.Value
				}
				if props["nvkr:toolchain"] != toolchain || props["nvkr:debianDistribution"] != distribution || props["nvkr:platform"] != platform {
					t.Fatalf("source target/toolchain differs from compiled route: %v", props)
				}
				if len(boms["agent"].Components) != 0 || index.Entries[0].ArtifactSHA != hash {
					t.Fatal("source declaration became inventory or changed archive subject")
				}
				if res.Components[0].ToolchainImage != "" || len(res.Components[0].ToolchainImages) != 0 {
					t.Fatal("source metadata projection mutated route declarations")
				}
			})
		}
	}
}

func TestDebianSourceDocumentRejectsChangedProducerBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*buildgraph.Resolved, *string)
		want   e.Code
	}{
		{"wrong Build", func(_ *buildgraph.Resolved, digest *string) { *digest = "sha256:" + strings.Repeat("f", 64) }, e.CodeUnverifiedRef},
		{"changed selected pin", func(r *buildgraph.Resolved, _ *string) {
			r.Components[0].Debian.Routes[2].ToolchainImage = "example.test/other@sha256:" + strings.Repeat("e", 64)
		}, e.CodeUnverifiedRef},
		{"invalid unselected argv", func(r *buildgraph.Resolved, _ *string) { r.Components[0].Debian.Routes[0].BuildCommand[0] = "bad\x00" }, e.CodeInvalidCatalog},
		{"missing target", func(r *buildgraph.Resolved, _ *string) { r.Debian = nil }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, digest, _ := debianSourceFixture(t, target.Debian{})
			tc.mutate(res, &digest)
			boms, index, err := PerArtifact(res, digest, map[string]string{"agent": "sha256:" + strings.Repeat("f", 64)})
			if sbomCode(err) != tc.want || boms != nil || index != nil {
				t.Fatalf("source projection = %v, %v, %v; want %s and no output", boms, index, err, tc.want)
			}
		})
	}
	if boms, index, err := PerArtifact(nil, "", nil); sbomCode(err) != e.CodeInvalidCatalog || boms != nil || index != nil {
		t.Fatal("nil description did not fail with no output", err)
	}
}
