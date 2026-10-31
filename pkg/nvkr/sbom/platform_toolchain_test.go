// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestSBOMSelectsToolchainForExecutablePlatform(t *testing.T) {
	amd64 := "golang@sha256:" + strings.Repeat("a", 64)
	arm64 := "golang@sha256:" + strings.Repeat("b", 64)
	subject := "sha256:" + strings.Repeat("c", 64)
	native := "sha256:" + strings.Repeat("d", 64)
	for _, tc := range []struct{ name, platform, toolchain, builder string }{
		{"linux-amd64", "linux/amd64", amd64, ""},
		{"linux-arm64", "linux/arm64", arm64, ""},
		{"darwin-arm64", "darwin/arm64", arm64, ""},
		{"darwin-native-go", "darwin/arm64", native, ir.BuilderNativeGo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := &buildgraph.Resolved{Platform: tc.platform, Components: []catalog.NamedComponent{{Name: "tool", Component: catalog.Component{
				Platforms: []string{"darwin/arm64", "linux/amd64", "linux/arm64"}, FromSource: true,
				Repo: "https://example.test/tool.git", Tag: "v1.0.0", Commit: strings.Repeat("1", 40), TreeHash: strings.Repeat("2", 40), SourceDate: 1,
				ToolchainImages: map[string]string{"amd64": amd64, "arm64": arm64}, BuildCommand: []string{"go", "build", "."}, ArtifactPath: "bin/tool", WriteDir: "bin", OutputType: "host-package",
			}}}}
			if tc.builder == ir.BuilderNativeGo {
				component := &resolved.Components[0]
				component.BuilderType, component.ToolchainImage, component.BuildNetwork, component.CGO = tc.builder, native, ir.NetworkDefault, true
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			builds := 0
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindBuild {
					builds++
					if node.Toolchain != tc.toolchain {
						t.Fatalf("compiled toolchain = %q, want %q", node.Toolchain, tc.toolchain)
					}
				}
			}
			if builds != 1 {
				t.Fatalf("compiled %d builds, want one", builds)
			}
			digest, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			hashes := map[string]string{"tool": subject}
			check := func(t *testing.T, boms map[string]*BOM, index *Index) {
				t.Helper()
				bom := boms["tool"]
				if bom == nil {
					t.Fatal("missing per-artifact SBOM")
				}
				if _, err := bom.Marshal(); err != nil {
					t.Fatal(err)
				}
				got := ""
				for _, p := range bom.Metadata.Component.Properties {
					if p.Name == "nvkr:toolchain" {
						got = p.Value
					}
				}
				if got != tc.toolchain {
					t.Fatalf("toolchain = %q, want %q", got, tc.toolchain)
				}
				if len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != subject {
					t.Fatal("SBOM lost observed subject")
				}
				if err := index.Validate(); err != nil {
					t.Fatal(err)
				}
				bd, err := bom.Digest()
				if err != nil {
					t.Fatal(err)
				}
				if index.BuildDigest != digest || len(index.Entries) != 1 || index.Entries[0].ArtifactSHA != subject || index.Entries[0].SBOMDigest != bd {
					t.Fatalf("index lost exact binding: %+v", index)
				}
			}
			t.Run("per-artifact", func(t *testing.T) {
				boms, index, err := PerArtifact(resolved, digest, hashes)
				if err != nil {
					t.Fatal(err)
				}
				check(t, boms, index)
			})
			t.Run("verified", func(t *testing.T) {
				set, err := BuildVerified(context.Background(), resolved, digest, hashes, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				boms, index, err := set.PerArtifact()
				if err != nil {
					t.Fatal(err)
				}
				check(t, boms, index)
				aggregate, err := set.Aggregate()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := aggregate.Marshal(); err != nil {
					t.Fatal(err)
				}
				got := ""
				for _, p := range aggregate.Components[0].Properties {
					if p.Name == "nvkr:toolchain" {
						got = p.Value
					}
				}
				if got != tc.toolchain {
					t.Fatalf("aggregate toolchain = %q, want %q", got, tc.toolchain)
				}
				if _, err := set.LocalReceipts(); sbomCode(err) != nvkrerr.CodePreflightFailed {
					t.Fatalf("descriptive SBOM gained publisher authority: %v", err)
				}
			})
			if tc.builder == ir.BuilderNativeGo {
				t.Run("missing-compiler", func(t *testing.T) {
					resolved.Components[0].ToolchainImage = ""
					if _, err := buildgraph.Compile(resolved); sbomCode(err) != nvkrerr.CodeEngineCapability {
						t.Fatalf("compiler-less native graph admitted: %v", err)
					}
					if _, _, err := PerArtifact(resolved, digest, hashes); sbomCode(err) != nvkrerr.CodeInvalidCatalog {
						t.Fatalf("native SBOM fell back to vendoring image: %v", err)
					}
					if _, err := BuildVerified(context.Background(), resolved, digest, hashes, nil, nil); sbomCode(err) != nvkrerr.CodeEngineCapability {
						t.Fatalf("verified native SBOM fell back to vendoring image: %v", err)
					}
				})
			}
		})
	}
}
