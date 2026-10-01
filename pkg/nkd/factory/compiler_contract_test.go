package factory

import (
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPackageOrderingUsesDependenciesAndRejectsCycles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []packageSpec
		want  []string
		fail  bool
	}{
		{"reverse dependency", []packageSpec{{Name: "a-root", Inputs: []string{"package/z-leaf"}}, {Name: "z-leaf", Inputs: []string{"worker/*"}}}, []string{"z-leaf", "a-root"}, false},
		{"independent lexical", []packageSpec{{Name: "z"}, {Name: "a"}}, []string{"a", "z"}, false},
		{"unknown package", []packageSpec{{Name: "root", Inputs: []string{"package/missing"}}}, nil, true},
		{"cycle", []packageSpec{{Name: "a", Inputs: []string{"package/b"}}, {Name: "b", Inputs: []string{"package/a"}}}, nil, true},
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
		{"Helm rejects binary", PackageHelm, "host-package", 1},
		{"Helm unary", PackageHelm, "tarball", 2},
		{"image needs OCI", PackageImage, "raw", 1},
		{"image unary", PackageImage, "oci-image", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := []string{"input"}
			if tc.count == 2 {
				inputs = append(inputs, "other")
			}
			artifacts := map[string]Artifact{"input": {Kind: tc.inputKind, BuildTarget: "worker"}, "other": {Kind: tc.inputKind, BuildTarget: "worker"}}
			assertFactoryErrorCode(t, validatePackageInputs(packageSpec{Name: "package", Kind: tc.kind}, inputs, artifacts), e.CodeInvalidCatalog)
		})
	}
	for _, target := range []string{TargetAPT, TargetObject} {
		assertFactoryErrorCode(t, validateRouteCompatibility(Route{Name: "route"}, Target{Kind: target}, Artifact{Kind: PackageAPT}), e.CodeEngineCapability)
	}
	assertFactoryErrorCode(t, validateRouteCompatibility(Route{Name: "route"}, Target{Kind: TargetOCI}, Artifact{Kind: "raw"}), e.CodeInvalidCatalog)
}

func TestCompilerSelectorsExpandWildcardsAndDeduplicate(t *testing.T) {
	artifacts := map[string]Artifact{
		"artifact/worker/z": {Component: "z"},
		"artifact/worker/a": {Component: "a"},
		"artifact/other/a":  {Component: "a"},
		"package/image":     {Kind: PackageImage},
	}
	for _, tc := range []struct {
		name            string
		selectors, want []string
	}{
		{"wildcard", []string{"worker/*", " worker/a "}, []string{"artifact/worker/a", "artifact/worker/z"}},
		{"explicit component and package", []string{"worker/a", "package/image"}, []string{"artifact/worker/a", "package/image"}},
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

func TestCompilerHelmBindingRequiresExactImageAndBuildSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*packageSpec, map[string]Artifact)
	}{
		{name: "valid"},
		{name: "wrong image identity", change: func(s *packageSpec, _ map[string]Artifact) { s.HelmImage.Image = "package/other" }},
		{name: "raw OCI is not image package", change: func(_ *packageSpec, a map[string]Artifact) { a["package/image"] = Artifact{Kind: "oci-image"} }},
		{name: "unbound source", change: func(_ *packageSpec, a map[string]Artifact) { a["artifact/source"] = Artifact{Kind: "tarball"} }},
		{name: "invalid chart", change: func(s *packageSpec, _ map[string]Artifact) { s.HelmImage.Chart = "../chart" }},
		{name: "wrong package type", change: func(s *packageSpec, _ map[string]Artifact) { s.Kind = PackageBinaryBundle }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := packageSpec{Name: "chart", Kind: PackageHelm, HelmImage: &HelmImageBinding{Image: "package/image", Chart: "controller"}}
			artifacts := map[string]Artifact{"package/image": {Kind: PackageImage}, "artifact/source": {Kind: "tarball", BuildTarget: "source"}}
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
