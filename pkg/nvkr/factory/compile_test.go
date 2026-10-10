// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func TestUnsetRunsOnDefaultsToGenericRunner(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	// No runsOn anywhere (definition or build target): ADR-18 §8 runs on a generic
	// runner, so compilation must DEFAULT the label rather than fail, and every emitted node
	// must carry the generic runner — no bespoke self-hosted label is ever required.
	definition, delivery := fixtureInputs("ai-factory", "node-worker", "")
	delivery.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	plan, err := Compile(context.Background(), definition, delivery, stack, "test")
	if err != nil {
		t.Fatalf("a definition without runsOn must compile (generic default), got: %v", err)
	}
	if len(plan.Graph.Nodes) == 0 {
		t.Fatal("expected a non-empty graph")
	}
	for _, node := range plan.Graph.Nodes {
		node, err = plan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		if node.RunsOn != DefaultRunsOn {
			t.Fatalf("node %q RunsOn = %q, want default %q", node.ID, node.RunsOn, DefaultRunsOn)
		}
		if node.ExecutionBinding != nil {
			t.Fatalf("unconstrained node %q received execution binding %#v", node.ID, node.ExecutionBinding)
		}
	}
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			if node.ExecutionBinding != nil {
				t.Fatalf("unconstrained IR node %q received execution binding %#v", node.ID, node.ExecutionBinding)
			}
		}
	}
}

func TestCompileBuildsExplicitArtifactAndDeliveryGraph(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("ai-factory", "node-worker", "ubuntu-24.04")
	definition.Outputs = append([]authored.DefinitionOutput{{Name: "node-apt", Platform: "linux/amd64", Format: authored.OutputAPT, Components: []string{"containerd"}, APT: &authored.APTOutput{Distribution: "stable", Component: "main"}}}, definition.Outputs...)
	delivery.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	plan, err := Compile(context.Background(), definition, delivery, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Qualification != TierStaticUnqualified || plan.CatalogDigest != stack.Digest {
		t.Fatalf("bad plan binding: %#v", plan)
	}
	byID := map[string]Node{}
	for _, node := range plan.Graph.Nodes {
		byID[node.ID] = node
	}
	build, err := plan.ExecutionFor(byID["node-worker/build-containerd"])
	if err != nil {
		t.Fatal(err)
	}
	if build.Kind != NodeBuild || len(build.Inputs) != 2 || build.Network != "none" ||
		!containsPrefix(build.Inputs, "source/node-worker/") || !containsPrefix(build.Inputs, "source-receipt/node-worker/") {
		t.Fatalf("containerd build lacks explicit isolated source input: %#v", build)
	}
	bundle := byID["package/node-worker"]
	if bundle.Kind != NodePackage || !contains(bundle.Inputs, "artifact/node-worker/containerd") || !contains(bundle.Inputs, "observation/node-worker") {
		t.Fatalf("bundle dataflow is incomplete: %#v", bundle)
	}
	publish := byID["publish/release"]
	if publish.Kind != NodePublish || len(publish.Inputs) != 1 || publish.Inputs[0] != "package/release" {
		t.Fatalf("publication does not consume exact package output: %#v", publish)
	}
	if _, err := plan.Digest(); err != nil {
		t.Fatal(err)
	}
	withOverride := definition
	withOverride.Outputs = append([]authored.DefinitionOutput(nil), definition.Outputs...)
	withOverride.Outputs[0].Catalog = &authored.Catalog{Base: "another-catalog"}
	if _, err := Compile(context.Background(), withOverride, delivery, stack, "test"); err == nil {
		t.Fatal("direct compiler silently ignored a build-specific catalog override")
	}
	if err := validateRouteCompatibility(Route{Name: "apt"}, Target{Kind: TargetAPT}, Artifact{Kind: PackageAPT}); err == nil {
		t.Fatal("accepted unavailable APT publisher")
	}

}

func TestPackageSpecsUseArtifactTopologyNotAuthoredOrLexicalOrder(t *testing.T) {
	ordered, err := orderPackageSpecs([]packageSpec{
		{Name: "aggregate", Inputs: []string{"package/z-bundle"}},
		{Name: "z-bundle", Inputs: []string{"build/component"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Name != "z-bundle" || ordered[1].Name != "aggregate" {
		t.Fatalf("package topology = %+v", ordered)
	}
	if _, err := orderPackageSpecs([]packageSpec{
		{Name: "a", Inputs: []string{"package/b"}},
		{Name: "b", Inputs: []string{"package/a"}},
	}); err == nil {
		t.Fatal("cyclic package artifact graph was accepted")
	}
}

func TestArtifactGraphRejectsMissingInputAndCycle(t *testing.T) {
	missing := ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes:     []Node{{ID: "build/a", Kind: NodeBuild, Network: "none", Inputs: []string{"artifact/missing"}, Outputs: []string{"artifact/a"}}},
		Artifacts: []Artifact{{ID: "artifact/a", Name: "a", Kind: "raw", Path: ".nvkr/a", Producer: "build/a"}},
	}
	if err := missing.Validate(); err == nil {
		t.Fatal("missing artifact input was accepted")
	}
	cycle := ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes: []Node{
			{ID: "build/a", Kind: NodeBuild, Network: "none", Inputs: []string{"artifact/b"}, Outputs: []string{"artifact/a"}},
			{ID: "build/b", Kind: NodeBuild, Network: "none", Inputs: []string{"artifact/a"}, Outputs: []string{"artifact/b"}},
		},
		Artifacts: []Artifact{
			{ID: "artifact/a", Name: "a", Kind: "raw", Path: ".nvkr/a", Producer: "build/a"},
			{ID: "artifact/b", Name: "b", Kind: "raw", Path: ".nvkr/b", Producer: "build/b"},
		},
	}
	if err := cycle.Validate(); err == nil {
		t.Fatal("artifact dataflow cycle was accepted")
	}
	unsafe := ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes: []Node{{ID: "package/a", Kind: NodePackage, Network: "none", RunsOn: "linux",
			Outputs: []string{"artifact/a"}}},
		Artifacts: []Artifact{{ID: "artifact/a", Name: "a", Kind: "binary-bundle", Path: "../escape",
			Producer: "package/a"}},
	}
	if err := unsafe.Validate(); err == nil {
		t.Fatal("unsafe artifact transport path was accepted")
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func testCatalogProvider(t *testing.T) catalog.DataProvider {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate factory test catalog")
	}
	return catalog.Dir(filepath.Join(filepath.Dir(source), "..", "..", "..", "testdata", "catalogs", "kubernetes-layered"))
}
