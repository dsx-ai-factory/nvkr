// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianCatalogFixture() *catalog.Catalog {
	image := "example.test/toolchain@sha256:" + strings.Repeat("c", 64)
	route := func(distribution, platform string) catalog.DebianRoute {
		return catalog.DebianRoute{Target: target.Debian{Distribution: distribution, Platform: platform},
			ToolchainImage: image, BuildCommand: []string{"make", "deb", "ARCH={{.Arch}}"},
			ArtifactPath: "out/package-{{.Arch}}.deb", WriteDir: "out"}
	}
	return &catalog.Catalog{Components: map[string]catalog.Component{"agent": {
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		Repo: "https://example.test/source", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		SourceDate: 1, OutputType: catalog.OutputDebianPackage, BuilderType: catalog.BuilderOCIHermetic,
		Debian: &catalog.DebianSpec{Routes: []catalog.DebianRoute{
			route("debian-13", "linux/amd64"), route("debian-13", "linux/arm64"),
			route("ubuntu-24.04", "linux/amd64"), route("ubuntu-24.04", "linux/arm64"),
		}},
	}}}
}

func resolveDebianFixture(t *testing.T, cat *catalog.Catalog, value *target.Debian) *Resolved {
	t.Helper()
	resolved, err := Resolve(cat, Selection{Components: []string{"agent"}, OutputFormat: catalog.OutputDebianPackage, Debian: value})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestDebianGraphBindsResolvedTargetAndSourceArchive(t *testing.T) {
	cat := debianCatalogFixture()
	implicit := resolveDebianFixture(t, cat, nil)
	explicit := resolveDebianFixture(t, cat, &target.Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"})
	graph := mustCompile(t, implicit)
	base, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	other, err := mustCompile(t, explicit).Digest()
	if err != nil || base != other {
		t.Fatal("explicit default changed logical identity", err)
	}
	for _, value := range []target.Debian{
		{Distribution: "debian-13", Platform: "linux/amd64"},
		{Distribution: "ubuntu-24.04", Platform: "linux/arm64"},
	} {
		changed, err := mustCompile(t, resolveDebianFixture(t, cat, &value)).Digest()
		if err != nil || changed == base {
			t.Fatal("target change did not change identity", err)
		}
	}
	node := graph.Nodes[0]
	if node.Kind != ir.KindBuild || node.Debian == nil || *node.Debian != *implicit.Debian ||
		node.OutputType != catalog.OutputDebianPackage || node.ArtifactPath != "out/package-amd64.deb" || node.Command[2] != "ARCH=amd64" {
		t.Fatalf("lowering replaced source archive/target: %+v", node)
	}
	// Source/catalog/request mutation cannot change a frozen resolved use or graph.
	cat.Components["agent"].Debian.Routes[2].BuildCommand[0] = "changed"
	implicit.Debian.Distribution = "debian-13"
	if graph.Nodes[0].Debian.Distribution != "ubuntu-24.04" || graph.Nodes[0].Command[0] != "make" {
		t.Fatal("graph aliases resolution")
	}
	if mustCompile(t, explicit).Nodes[0].Command[0] != "make" {
		t.Fatal("resolution aliases catalog routes")
	}
	// Transport mirrors do not alter the logical producer identity.
	mirrored := debianCatalogFixture()
	c := mirrored.Components["agent"]
	c.Repo = "ssh://mirror.example/source"
	for i := range c.Debian.Routes {
		c.Debian.Routes[i].ToolchainImage = "mirror.example/toolchain@sha256:" + strings.Repeat("c", 64)
	}
	mirrored.Components["agent"] = c
	mirrorDigest, err := mustCompile(t, resolveDebianFixture(t, mirrored, nil)).Digest()
	if err != nil || mirrorDigest != base {
		t.Fatal("transport mirror changed Build identity", err)
	}
}

func TestDebianGraphRejectsAmbiguousAndUnjoinedInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection Selection
		want      e.Code
	}{
		{"missing source", Selection{Components: []string{"missing"}, OutputFormat: catalog.OutputDebianPackage}, e.CodeInvalidCatalog},
		{"multiple sources", Selection{Components: []string{"agent", "other"}, OutputFormat: catalog.OutputDebianPackage}, e.CodeInvalidCatalog},
		{"conflicting platform", Selection{Components: []string{"agent"}, OutputFormat: catalog.OutputDebianPackage, Platform: "linux/amd64", Debian: &target.Debian{Platform: "linux/arm64"}}, e.CodeInvalidCatalog},
		{"target on old output", Selection{Components: []string{"agent"}, Platform: "linux/amd64", Debian: &target.Debian{}}, e.CodeInvalidCatalog},
		{"old output selects Debian", Selection{Components: []string{"agent"}, Platform: "linux/amd64"}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := Resolve(debianCatalogFixture(), tc.selection)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.want || resolved != nil {
				t.Fatalf("admission = %v, %v", resolved, err)
			}
		})
	}
	for _, mutate := range []func(*Resolved){
		func(r *Resolved) { r.Debian = nil },
		func(r *Resolved) { r.Debian.Platform = "linux/arm64" },
		func(r *Resolved) { r.Debian.Distribution = "unknown" },
	} {
		r := resolveDebianFixture(t, debianCatalogFixture(), nil)
		mutate(r)
		if graph, err := Compile(r); code(err) != e.CodeInvalidCatalog || graph != nil {
			t.Fatal("mutated resolution compiled")
		}
	}
}

func TestDebianExecutionWaitsForFinalByteEvidenceJoin(t *testing.T) {
	graph := mustCompile(t, resolveDebianFixture(t, debianCatalogFixture(), nil))
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "absent-workspace")
	for _, only := range [][]string{nil, {"agent"}, {graph.Nodes[1].ID}} {
		_, err := (pipeline.Local{}).Run(graph, digest, pipeline.RunOptions{Ctx: t.Context(), SourceRoot: root, Only: only})
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
			t.Fatalf("execution admission = %v", err)
		}
	}
	result, err := (pipeline.Local{}).ExecNode(graph.Nodes[0], pipeline.RunOptions{Ctx: t.Context(), SourceRoot: root})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || result != nil {
		t.Fatalf("node admission = %v, %v", result, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("closed lane performed workspace I/O")
	}
}

func TestSelectedDebianRouteGuards(t *testing.T) {
	for _, mode := range []string{"missing selection", "missing declaration", "no matching route"} {
		t.Run(mode, func(t *testing.T) {
			component := catalog.NamedComponent{Name: "agent", Component: debianCatalogFixture().Components["agent"]}
			selected := &target.Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"}
			if mode == "missing selection" {
				selected = nil
			}
			if mode == "missing declaration" {
				component.Debian = nil
			}
			if mode == "no matching route" {
				selected.Distribution = "unknown"
			}
			_, err := selectedDebianRoute(component, selected)
			if code(err) != e.CodeInvalidCatalog {
				t.Fatal(err)
			}
		})
	}
}
