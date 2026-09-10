package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestClusterCompilerBindsExactRecipeAndSelectedRole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Plan, *CompileSpec)
		code   e.Code
	}{
		{name: "inferred role subset"},
		{name: "explicit role subset", change: func(p *Plan, d *CompileSpec) {
			d.Clusters[0].RoleBindings[0].Subset = append([]string(nil), p.Recipes[0].Components...)
		}},
		{name: "wrong recipe digest", change: func(p *Plan, _ *CompileSpec) { p.Recipes[0].RecipeDigest = "sha256:" + strings.Repeat("a", 64) }, code: e.CodeUnverifiedRef},
		{name: "no selected role", change: func(_ *Plan, d *CompileSpec) { d.Clusters[0].RoleBindings[0].Role = "gpu-worker" }, code: e.CodeInvalidCatalog},
		{name: "mixed resolution frames", change: func(p *Plan, d *CompileSpec) {
			r := p.Recipes[0]
			r.Name = "other"
			r.Criteria.Arch = "arm64"
			p.Recipes = append(p.Recipes, r)
			d.Clusters[0].RecipeTargets = append(d.Clusters[0].RecipeTargets, "other")
		}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			definition := CompileSpec{Clusters: []ClusterSpec{{Name: "tenant", Type: cluster.TypeTenant, RecipeTargets: []string{"worker"}, RoleBindings: []cluster.RoleBinding{{Role: "worker", NodeLevelCategories: []string{"kubernetes"}}}}}}
			if tc.change != nil {
				tc.change(plan, &definition)
			}
			nodes, artifacts := map[string]Node{}, map[string]Artifact{}
			err := addClusterCompositions(definition, plan, nodes, artifacts)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Compositions) != 1 {
				t.Fatalf("compositions = %+v", plan.Compositions)
			}
			composition := plan.Compositions[0]
			if !reflect.DeepEqual(composition.Composition.RoleBindings[0].Subset, plan.Recipes[0].Components) || composition.Composition.Recipes[0].RecipeDigest != plan.Recipes[0].RecipeDigest {
				t.Fatalf("composition lost recipe/SKU bindings: %+v", composition)
			}
			t.Chdir(t.TempDir())
			if err := executeComposition(t.Context(), plan, nodes["composition/tenant"], artifacts); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.FromSlash(artifacts["composition/tenant"].Path))
			if err != nil {
				t.Fatal(err)
			}
			var emitted cluster.ClusterComposition
			if err := json.Unmarshal(raw, &emitted); err != nil {
				t.Fatal(err)
			}
			digest, err := emitted.Digest()
			if err != nil || digest != composition.Digest {
				t.Fatalf("emitted composition identity = %s, %v", digest, err)
			}
		})
	}
}

func TestPackageOrderingUsesDependenciesAndRejectsCycles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []PackageSpec
		want  []string
		fail  bool
	}{
		{"reverse dependency", []PackageSpec{{Name: "a-root", Inputs: []string{"package/z-leaf"}}, {Name: "z-leaf", Inputs: []string{"worker/*"}}}, []string{"z-leaf", "a-root"}, false},
		{"independent lexical", []PackageSpec{{Name: "z"}, {Name: "a"}}, []string{"a", "z"}, false},
		{"unknown package", []PackageSpec{{Name: "root", Inputs: []string{"package/missing"}}}, nil, true},
		{"cycle", []PackageSpec{{Name: "a", Inputs: []string{"package/b"}}, {Name: "b", Inputs: []string{"package/a"}}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := orderPackageSpecs(tc.specs)
			if tc.fail {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, spec := range got {
				names = append(names, spec.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("order = %v, want %v", names, tc.want)
			}
		})
	}
}

func TestCompilerPackageAndRouteAdmissionRejectsWrongRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name, kind, inputKind string
		count                 int
	}{
		{"APT needs Debian", PackageAPT, "raw", 1},
		{"Helm needs source", PackageHelm, "raw", 1},
		{"Helm unary", PackageHelm, "tarball", 2},
		{"image needs OCI", PackageImage, "raw", 1},
		{"image unary", PackageImage, "oci-image", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := []string{"input"}
			if tc.count == 2 {
				inputs = append(inputs, "other")
			}
			artifacts := map[string]Artifact{"input": {Kind: tc.inputKind, RecipeTarget: "worker"}, "other": {Kind: tc.inputKind, RecipeTarget: "worker"}}
			assertFactoryErrorCode(t, validatePackageInputs(PackageSpec{Name: "package", Kind: tc.kind}, inputs, artifacts), e.CodeInvalidCatalog)
		})
	}
	for _, target := range []string{TargetAPT, TargetObject} {
		assertFactoryErrorCode(t, validateRouteCompatibility(Route{Name: "route"}, Target{Kind: target}, Artifact{Kind: PackageAPT}), e.CodeEngineCapability)
	}
	assertFactoryErrorCode(t, validateRouteCompatibility(Route{Name: "route"}, Target{Kind: TargetOCI}, Artifact{Kind: "raw"}), e.CodeInvalidCatalog)
}

func TestCompilerSelectorsKeepAICRExplicitAndDeduplicate(t *testing.T) {
	artifacts := map[string]Artifact{
		"artifact/worker/z":    {Component: "z"},
		"artifact/worker/a":    {Component: "a"},
		"artifact/worker/aicr": {Component: "aicr-bundle"},
		"artifact/other/a":     {Component: "a"},
		"package/image":        {Kind: PackageImage},
	}
	for _, tc := range []struct {
		name            string
		selectors, want []string
	}{
		{"wildcard excludes generated AICR", []string{"worker/*", " worker/a "}, []string{"artifact/worker/a", "artifact/worker/z"}},
		{"explicit AICR and package", []string{"worker/aicr", "package/image"}, []string{"artifact/worker/aicr", "package/image"}},
		{"missing exact", []string{"worker/missing"}, nil},
		{"empty wildcard", []string{"missing/*"}, nil},
		{"partial failure returns no selection", []string{"worker/a", "missing"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandSelectors(tc.selectors, artifacts)
			if (err != nil) != (tc.want == nil) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selection = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestCompilerHelmBindingRequiresExactImageAndRecipeSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PackageSpec, map[string]Artifact)
	}{
		{name: "valid"},
		{name: "wrong image identity", change: func(s *PackageSpec, _ map[string]Artifact) { s.HelmImage.Image = "package/other" }},
		{name: "raw OCI is not image package", change: func(_ *PackageSpec, a map[string]Artifact) { a["package/image"] = Artifact{Kind: "oci-image"} }},
		{name: "unbound source", change: func(_ *PackageSpec, a map[string]Artifact) { a["artifact/source"] = Artifact{Kind: "tarball"} }},
		{name: "invalid chart", change: func(s *PackageSpec, _ map[string]Artifact) { s.HelmImage.Chart = "../chart" }},
		{name: "wrong package type", change: func(s *PackageSpec, _ map[string]Artifact) { s.Kind = PackageBinaryBundle }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := PackageSpec{Name: "chart", Kind: PackageHelm, HelmImage: &HelmImageBinding{Image: "package/image", Chart: "controller"}}
			artifacts := map[string]Artifact{"package/image": {Kind: PackageImage}, "artifact/source": {Kind: "tarball", RecipeTarget: "source"}}
			if tc.change != nil {
				tc.change(&spec, artifacts)
			}
			err := validatePackageInputs(spec, []string{"artifact/source", "package/image"}, artifacts)
			if tc.change != nil {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
