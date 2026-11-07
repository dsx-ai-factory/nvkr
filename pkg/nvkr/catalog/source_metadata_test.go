// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceMetadataBindings(t *testing.T) {
	for _, tc := range []struct {
		name, tag, commit, stream string
	}{
		{"tagged", "v2.4.2", strings.Repeat("a", 40), "{{.ReleaseVersion}}"},
		{"module tag", "module/v2.4.2", strings.Repeat("b", 40), "{{.ReleaseVersion}}"},
		{"explicit commit stream", strings.Repeat("c", 40), strings.Repeat("c", 40), "2.4.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Component{FromSource: true, BuilderType: BuilderOCIHermetic, Tag: tc.tag, Commit: tc.commit,
				SourceMetadata: map[string]string{
					"NVKR_SOURCE_COMMIT": "{{.Commit}}", "NVKR_SOURCE_STREAM": tc.stream,
					"NVKR_SOURCE_REVISION": `1+git.{{printf "%.12s" .Commit}}`,
				}}
			got, err := c.SourceMetadataEnv()
			if err != nil {
				t.Fatal(err)
			}
			if got["NVKR_SOURCE_COMMIT"] != tc.commit || got["NVKR_SOURCE_STREAM"] != "2.4.2" || got["NVKR_SOURCE_REVISION"] != "1+git."+tc.commit[:12] {
				t.Fatalf("source bindings = %#v", got)
			}
		})
	}
}

func TestSourceMetadataRejectsConflictsAndInvalidBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Component)
	}{
		{"reserved stamp", func(c *Component) { c.VersionStamp = map[string]string{"NVKR_SOURCE_COMMIT": "forged"} }},
		{"reproducibility override", func(c *Component) { c.SourceMetadata = map[string]string{"SOURCE_DATE_EPOCH": "1"} }},
		{"arbitrary environment", func(c *Component) { c.SourceMetadata = map[string]string{"PATH": "/tmp"} }},
		{"unknown token", func(c *Component) { c.SourceMetadata["NVKR_SOURCE_COMMIT"] = "{{.Unknown}}" }},
		{"nul", func(c *Component) { c.SourceMetadata["NVKR_SOURCE_COMMIT"] = "\x00" }},
		{"oversized rendering", func(c *Component) { c.SourceMetadata["NVKR_SOURCE_COMMIT"] = `{{printf "%5000s" .Commit}}` }},
		{"commit release", func(c *Component) { c.Tag = c.Commit; c.SourceMetadata["NVKR_SOURCE_STREAM"] = "{{.ReleaseVersion}}" }},
		{"non-semantic release", func(c *Component) { c.Tag = "next"; c.SourceMetadata["NVKR_SOURCE_STREAM"] = "{{.ReleaseVersion}}" }},
		{"acquired producer", func(c *Component) { c.FromSource = false }},
		{"builder without environment", func(c *Component) { c.BuilderType = BuilderVMImage }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Component{FromSource: true, BuilderType: BuilderOCIHermetic, Tag: "v2.4.2", Commit: strings.Repeat("a", 40),
				SourceMetadata: map[string]string{"NVKR_SOURCE_COMMIT": "{{.Commit}}"}}
			tc.edit(&c)
			_, err := c.SourceMetadataEnv()
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, e.CodeInvalidCatalog)
			}
		})
	}
}

func TestSourceMetadataOverlayBuilderDefault(t *testing.T) {
	for _, builder := range []string{"", BuilderOCIHermetic} {
		t.Run("builder="+builder, func(t *testing.T) {
			stack := sourceRevisionStackFixture(t)
			component := stack.Catalog.Components["kubectl"]
			component.BuilderType = builder
			component.SourceMetadata = map[string]string{"NVKR_SOURCE_COMMIT": "{{.Commit}}"}
			resolved, err := ApplyStack(stack.Catalog, Overlay{
				APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "metadata",
				RequiresCatalogDigest: stack.Digest,
				Components:            map[string]ComponentReplacement{"kubectl": {Component: &component}},
			})
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := resolved.Catalog.Components["kubectl"].SourceMetadataEnv()
			if err != nil || metadata["NVKR_SOURCE_COMMIT"] != component.Commit {
				t.Fatalf("overlay metadata: %v %v", metadata, err)
			}
		})
	}
}
