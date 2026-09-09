package factory

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
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
	// No runsOn anywhere (definition or recipe target): ADR-18 §8 runs on a generic
	// runner, so compilation must DEFAULT the label rather than fail, and every emitted node
	// must carry the generic runner — no bespoke self-hosted label is ever required.
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "ai-factory"}, Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "node-worker", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{{Name: "node-tools", Kind: PackageBinaryBundle, Inputs: []string{"node-worker/*"}}},
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "env"},
		Targets:  map[string]Target{"oci": {Kind: TargetOCI, Ref: "localhost:5000/nkd/tools:dev", Anonymous: true, PlainHTTP: true}},
		Routes:   []Route{{Name: "node-tools", Input: "package/node-tools", Target: "oci"}},
	}
	plan, err := Compile(context.Background(), definition, delivery, stack, "test")
	if err != nil {
		t.Fatalf("a definition without runsOn must compile (generic default), got: %v", err)
	}
	if len(plan.Graph.Nodes) == 0 {
		t.Fatal("expected a non-empty graph")
	}
	for _, node := range plan.Graph.Nodes {
		if node.RunsOn != DefaultRunsOn {
			t.Fatalf("node %q RunsOn = %q, want default %q", node.ID, node.RunsOn, DefaultRunsOn)
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
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "ai-factory"}, RunsOn: "ubuntu-24.04", Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "node-worker", RunsOn: "ubuntu-24.04", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{
			{Name: "node-tools", Kind: PackageBinaryBundle, Inputs: []string{"node-worker/*"}},
			{Name: "node-apt", Kind: PackageAPT, Inputs: []string{"node-worker/containerd"}, Distribution: "stable", Component: "main"},
		},
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "laptop"},
		Targets: map[string]Target{
			"oci": {Kind: TargetOCI, Ref: "localhost:5000/nkd/tools:dev", Anonymous: true, PlainHTTP: true},
		},
		Routes: []Route{
			{Name: "node-tools", Input: "package/node-tools", Target: "oci"},
		},
	}
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
	build := byID["node-worker/build-containerd"]
	if build.Kind != NodeBuild || len(build.Inputs) != 2 || build.Network != "none" ||
		!containsPrefix(build.Inputs, "source/node-worker/") || !containsPrefix(build.Inputs, "source-receipt/node-worker/") {
		t.Fatalf("containerd build lacks explicit isolated source input: %#v", build)
	}
	bundle := byID["package/node-tools"]
	if bundle.Kind != NodePackage || !contains(bundle.Inputs, "artifact/node-worker/containerd") || !contains(bundle.Inputs, "observation/node-worker") {
		t.Fatalf("bundle dataflow is incomplete: %#v", bundle)
	}
	publish := byID["publish/node-tools"]
	if publish.Kind != NodePublish || len(publish.Inputs) != 1 || publish.Inputs[0] != "package/node-tools" {
		t.Fatalf("publication does not consume exact package output: %#v", publish)
	}
	if _, err := plan.Digest(); err != nil {
		t.Fatal(err)
	}
	withOverride := definition
	withOverride.Recipes = append([]RecipeTarget(nil), definition.Recipes...)
	withOverride.Recipes[0].Catalog = &CatalogStack{Base: "another-catalog"}
	if _, err := Compile(context.Background(), withOverride, delivery, stack, "test"); err == nil {
		t.Fatal("direct compiler silently ignored a recipe-specific catalog override")
	}
	delivery.Targets["apt"] = Target{Kind: TargetAPT, Ref: "https://apt.example.invalid/nkd", Anonymous: true}
	delivery.Routes = []Route{{Name: "node-apt", Input: "package/node-apt", Target: "apt"}}
	if _, err := Compile(context.Background(), definition, delivery, stack, "test"); err == nil {
		t.Fatal("factory accepted an APT route without an implemented remote publication adapter")
	}
}

func TestPackageSpecsUseArtifactTopologyNotAuthoredOrLexicalOrder(t *testing.T) {
	ordered, err := orderPackageSpecs([]PackageSpec{
		{Name: "aggregate", Inputs: []string{"package/z-bundle"}},
		{Name: "z-bundle", Inputs: []string{"recipe/component"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Name != "z-bundle" || ordered[1].Name != "aggregate" {
		t.Fatalf("package topology = %+v", ordered)
	}
	if _, err := orderPackageSpecs([]PackageSpec{
		{Name: "a", Inputs: []string{"package/b"}},
		{Name: "b", Inputs: []string{"package/a"}},
	}); err == nil {
		t.Fatal("cyclic package artifact graph was accepted")
	}
}

func TestArtifactGraphRejectsMissingInputAndCycle(t *testing.T) {
	missing := ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes:     []Node{{ID: "build/a", Kind: NodeBuild, Network: "none", Inputs: []string{"artifact/missing"}, Outputs: []string{"artifact/a"}}},
		Artifacts: []Artifact{{ID: "artifact/a", Name: "a", Kind: "raw", Path: ".nkd/a", Producer: "build/a"}},
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
			{ID: "artifact/a", Name: "a", Kind: "raw", Path: ".nkd/a", Producer: "build/a"},
			{ID: "artifact/b", Name: "b", Kind: "raw", Path: ".nkd/b", Producer: "build/b"},
		},
	}
	if err := cycle.Validate(); err == nil {
		t.Fatal("artifact dataflow cycle was accepted")
	}
	unsafe := ArtifactGraph{SchemaVersion: ArtifactGraphSchema,
		Nodes: []Node{{ID: "composition/a", Kind: NodeComposition, Network: "none", RunsOn: "linux",
			Outputs: []string{"artifact/a"}}},
		Artifacts: []Artifact{{ID: "artifact/a", Name: "a", Kind: "composition", Path: "../escape",
			Producer: "composition/a"}},
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
