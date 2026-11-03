// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func pinnedToolCatalog() *catalog.Catalog {
	tool := catalog.ToolPin{Format: "tar-gzip", Platform: "linux/arm64", ExecutablePath: "aicr",
		URL:           "https://github.com/NVIDIA/aicr/releases/download/v1.2.0-rc1/aicr_1.2.0-rc1_linux_arm64.tar.gz",
		ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}
	return &catalog.Catalog{Components: map[string]catalog.Component{
		"recipe": {Platforms: []string{"linux/arm64"}, BuilderType: catalog.BuilderPinnedTool, Tool: "aicr", ToolExecutable: &tool,
			ToolArgs: []string{"recipe", "--accelerator", "gb200", "--output", "{{.Output}}/recipe.yaml"},
			ToolEnv:  map[string]string{"AICR_LOG_FORMAT": "text"}, Tag: "v1.2.0-rc1",
			ToolchainImage: "example.test/runner@sha256:" + strings.Repeat("c", 64),
			OutputType:     catalog.PinnedToolOutputFile, ArtifactPath: "recipe.yaml"},
		"consumer": {Platforms: []string{"linux/arm64"}, FromSource: true, Repo: "https://example.test/source", Tag: "v1.0.0",
			Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
			ToolchainImage: "example.test/toolchain@sha256:" + strings.Repeat("d", 64),
			OutputType:     "raw", BuildCommand: []string{"make"}, WriteDir: "out", ArtifactPath: "out/artifact", SourceDate: 1,
			Materials: map[string]string{"recipe": "recipe"}},
	}}
}

func TestPinnedToolCompilesSourceLessBuild(t *testing.T) {
	resolved, err := Resolve(pinnedToolCatalog(), Selection{Components: []string{"consumer", "recipe"}, Platform: "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	graph := mustCompile(t, resolved)
	nodes := map[string]ir.Node{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	recipe := nodes["build-recipe"]
	if recipe.Kind != ir.KindBuild || recipe.BuilderType != ir.BuilderPinnedTool || len(recipe.Needs) != 0 ||
		recipe.SourceDest != "" || recipe.Network != ir.NetworkNone || recipe.Tool != "aicr" ||
		recipe.Digest != "sha256:"+strings.Repeat("b", 64) || recipe.ExecutableArchive.Format != "tar-gzip" ||
		!slices.Equal(recipe.Command, []string{"recipe", "--accelerator", "gb200", "--output", ir.PinnedToolOutputDir + "/recipe.yaml"}) ||
		recipe.Version != "1.2.0-rc1" {
		t.Fatalf("wrong pinned-tool node: %+v", recipe)
	}
	if consumer := nodes["build-consumer"]; !slices.Contains(consumer.Needs, "build-recipe") {
		t.Fatalf("consumer lost its exact material edge: %+v", consumer)
	}
	for id := range nodes {
		if strings.HasPrefix(id, "fetch-") && !slices.Contains(nodes["build-consumer"].Needs, id) {
			t.Fatalf("pinned-tool producer gained a source fetch %q", id)
		}
	}
}

// A resolved pinned-tool producer owns its tool pin, arguments and environment in both
// directions, so a later catalog change cannot alter an already resolved selection.
func TestPinnedToolResolvedInputsOwnTheirMemory(t *testing.T) {
	cat := pinnedToolCatalog()
	recipe := cat.Components["recipe"]
	recipe.ToolExecutable.Companions = []catalog.ToolCompanion{{Path: "aicr.sig", Digest: "sha256:" + strings.Repeat("d", 64)}}
	resolved, err := Resolve(cat, Selection{Components: []string{"consumer", "recipe"}, Platform: "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	var got catalog.Component
	for _, component := range resolved.Components {
		if component.Name == "recipe" {
			got = component.Component
		}
	}
	snapshot := func(c catalog.Component) string { return fmt.Sprint(*c.ToolExecutable, c.ToolArgs, c.ToolEnv) }
	mutate := func(c catalog.Component, value string) {
		c.ToolExecutable.ExecutableDigest = "sha256:" + strings.Repeat(value, 64)
		c.ToolExecutable.Companions[0].Digest = "sha256:" + strings.Repeat(value, 64)
		c.ToolArgs[2], c.ToolEnv["AICR_LOG_FORMAT"] = value, value
	}
	catalogBefore := snapshot(recipe)
	mutate(got, "e")
	if snapshot(recipe) != catalogBefore {
		t.Fatalf("resolved mutation changed the catalog: %s", snapshot(recipe))
	}
	resolvedBefore := snapshot(got)
	mutate(recipe, "f")
	if snapshot(got) != resolvedBefore {
		t.Fatalf("catalog mutation changed the resolved producer: %s", snapshot(got))
	}
}

// Every logical field in ADR-27 §2 changes the Build Digest; the release URL does not.
func TestPinnedToolBuildIdentity(t *testing.T) {
	digest := func(change func(*catalog.Component)) string {
		cat := pinnedToolCatalog()
		recipe := cat.Components["recipe"]
		tool := *recipe.ToolExecutable
		recipe.ToolExecutable = &tool
		recipe.ToolArgs = slices.Clone(recipe.ToolArgs)
		recipe.ToolEnv = maps.Clone(recipe.ToolEnv)
		if change != nil {
			change(&recipe)
		}
		cat.Components["recipe"] = recipe
		resolved, err := Resolve(cat, Selection{Components: []string{"consumer", "recipe"}, Platform: "linux/arm64"})
		if err != nil {
			t.Fatal(err)
		}
		value, err := mustCompile(t, resolved).Digest()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	base := digest(nil)
	for _, test := range []struct {
		name    string
		change  func(*catalog.Component)
		changes bool
	}{
		{"release URL is transport", func(c *catalog.Component) { c.ToolExecutable.URL = "https://mirror.example/aicr.tar.gz" }, false},
		{"executable digest", func(c *catalog.Component) { c.ToolExecutable.ExecutableDigest = "sha256:" + strings.Repeat("e", 64) }, true},
		{"archive digest", func(c *catalog.Component) { c.ToolExecutable.ArchiveDigest = "sha256:" + strings.Repeat("f", 64) }, true},
		{"selected member", func(c *catalog.Component) { c.ToolExecutable.ExecutablePath = "bin/aicr" }, true},
		{"archive format", func(c *catalog.Component) { c.ToolExecutable.Format = "zip" }, true},
		{"tool name", func(c *catalog.Component) { c.Tool = "aicr-arm64" }, true},
		{"arguments", func(c *catalog.Component) { c.ToolArgs[2] = "gb300" }, true},
		{"literal environment", func(c *catalog.Component) { c.ToolEnv["AICR_LOG_FORMAT"] = "json" }, true},
		{"execution image", func(c *catalog.Component) {
			c.ToolchainImage = "example.test/runner@sha256:" + strings.Repeat("9", 64)
		}, true},
		{"execution image registry is transport", func(c *catalog.Component) {
			c.ToolchainImage = "mirror.example/runner@sha256:" + strings.Repeat("c", 64)
		}, false},
		{"output path", func(c *catalog.Component) {
			c.ArtifactPath = "r.yaml"
			c.ToolArgs[4] = "{{.Output}}/r.yaml"
		}, true},
	} {
		if got := digest(test.change); (got != base) != test.changes {
			t.Errorf("%s: digest changed=%t, want %t", test.name, got != base, test.changes)
		}
	}
}

func TestPinnedToolOutputAdmission(t *testing.T) {
	layout := func(cat *catalog.Catalog) {
		bundle := cat.Components["recipe"]
		bundle.OutputType, bundle.ArtifactPath = catalog.PinnedToolOutputOCILayout, ""
		bundle.ToolArgs = []string{"bundle", "--output", "oci-layout:{{.Output}}"}
		cat.Components["bundle"] = bundle
	}
	for _, test := range []struct {
		name     string
		change   func(*catalog.Catalog)
		selected []string
		format   string
		code     nvkrerr.Code
	}{
		{name: "material closure", selected: []string{"consumer", "recipe"}},
		{name: "unbound file producer", selected: []string{"recipe"}, code: nvkrerr.CodeInvalidCatalog},
		{name: "unselected material", selected: []string{"consumer"}, code: nvkrerr.CodeInvalidCatalog},
		{name: "executable bundle with its material", selected: []string{"consumer", "recipe"}, format: definition.OutputExecutable},
		{name: "layout producer without bundle-oci", change: layout, selected: []string{"bundle"}, code: nvkrerr.CodeInvalidCatalog},
		{name: "wrong target", selected: []string{"consumer", "recipe"}, code: nvkrerr.CodeEngineCapability, change: func(cat *catalog.Catalog) {
			for name, component := range cat.Components {
				component.Platforms = []string{"linux/amd64"}
				cat.Components[name] = component
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat := pinnedToolCatalog()
			if test.change != nil {
				test.change(cat)
			}
			platform := "linux/arm64"
			_, err := Resolve(cat, Selection{Components: test.selected, Platform: platform, OutputFormat: test.format})
			if code(err) != test.code {
				t.Fatalf("Resolve() error %v; want %s", err, test.code)
			}
		})
	}
}

// A layout writer may refuse an existing directory, so a layout producer's output
// token names an absent child of the private output root.
func TestPinnedToolLayoutOutputRendersAnAbsentPath(t *testing.T) {
	cat := pinnedToolCatalog()
	bundle := cat.Components["recipe"]
	bundle.OutputType, bundle.ArtifactPath = catalog.PinnedToolOutputOCILayout, ""
	bundle.ToolArgs = []string{"bundle", "--output", "oci-layout:{{.Output}}"}
	graph := mustCompile(t, &Resolved{Platform: "linux/arm64", Components: []catalog.NamedComponent{{Name: "bundle", Component: bundle}}})
	if got := graph.Nodes[0].Command; !slices.Equal(got, []string{"bundle", "--output", "oci-layout:" + ir.PinnedToolLayoutDir}) {
		t.Fatalf("layout output rendered %q", got)
	}
}
