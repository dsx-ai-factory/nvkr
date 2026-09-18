package lifecycle

import (
	"path/filepath"
	"slices"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestHelmImageDependency(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))
	definitionPath := filepath.Join("definitions", "nke-v1", "definition.yaml")
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), filepath.Join(filepath.Dir(definitionPath), "environment-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definitionPath, definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	var charts factory.Node
	for _, node := range plan.Graph.Nodes {
		if node.ID == "package/nke-helm" {
			charts = node
		}
	}
	if charts.HelmImage == nil || charts.HelmImage.Image != "package/cwe" || !slices.Contains(charts.Inputs, "package/cwe") || !slices.Contains(charts.Inputs, "artifact/nke-helm/nke-helm") {
		t.Fatalf("image dependency: %#v", charts)
	}
	definition.Outputs[1].Helm.ImageOutput = "missing"
	assertCode(t, definition.Validate(), e.CodeInvalidCatalog)
}

func TestSourceHelmSelection(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))
	for _, tc := range []struct {
		name       string
		components []string
		valid      bool
	}{
		{"one source archive", []string{"nke-helm"}, true},
		{"missing source archive", nil, false},
		{"ambiguous source archives", []string{"first", "second"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definitionPath := filepath.Join("definitions", "nke-v1", "definition.yaml")
			definition, err := LoadDefinition(t.Context(), definitionPath)
			if err != nil {
				t.Fatal(err)
			}
			definition.Outputs = []DefinitionOutput{{Name: "charts", Format: OutputHelm, Platform: "linux/amd64", Components: tc.components}}
			err = definition.Validate()
			if !tc.valid {
				assertCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), definitionPath, definition, "environment.yaml", testEnvironment(), "test")
			if err != nil {
				t.Fatal(err)
			}
			var charts factory.Node
			for _, node := range plan.Graph.Nodes {
				if node.ID == "package/charts" {
					charts = node
				}
			}
			if charts.PackageKind != factory.PackageHelm || charts.HelmImage != nil || !slices.Contains(charts.Inputs, "artifact/charts/nke-helm") {
				t.Fatalf("source Helm selection lost: %#v", charts)
			}
		})
	}
}
