package lifecycle

import (
	"sort"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
)

func TestHelmImageDependency(t *testing.T) {
	definition := testDefinition(t)
	definition.Clusters = nil
	definition.Outputs = append(definition.Outputs, DefinitionOutput{Name: "charts", Recipe: "cli-recipe", Format: OutputHelm, Components: []string{"charts"}, Helm: &HelmOutput{ImageOutput: "cli", Chart: "007-controller"}})
	definition.Outputs[0].Format = OutputOCI
	sort.Slice(definition.Outputs, func(i, j int) bool { return definition.Outputs[i].Name < definition.Outputs[j].Name })
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	spec, _, err := lower(definition, testEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	pkg := spec.Packages[0]
	if pkg.HelmImage == nil || pkg.HelmImage.Image != "package/cli" || len(pkg.Inputs) != 2 {
		t.Fatalf("image dependency: %#v", pkg)
	}
	definition.Outputs[0].Helm.ImageOutput = "missing"
	assertCode(t, definition.Validate(), e.CodeInvalidCatalog)
}

func TestSourceHelmSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		components []string
		valid      bool
	}{
		{"one source archive", []string{"nkectl"}, true},
		{"missing source archive", nil, false},
		{"ambiguous source archives", []string{"first", "second"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := testDefinition(t)
			definition.Outputs[0].Format = OutputHelm
			definition.Outputs[0].Components = tc.components
			err := definition.Validate()
			if !tc.valid {
				assertCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			spec, _, err := lower(definition, testEnvironment())
			if err != nil {
				t.Fatal(err)
			}
			pkg := spec.Packages[0]
			if pkg.Kind != factory.PackageHelm || len(pkg.Inputs) != 1 || pkg.Inputs[0] != definition.Outputs[0].Recipe+"/nkectl" {
				t.Fatalf("source Helm selection lost: %#v", pkg)
			}
		})
	}
}
