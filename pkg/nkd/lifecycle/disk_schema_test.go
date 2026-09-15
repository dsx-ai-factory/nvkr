package lifecycle

import (
	"context"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func diskDefinition(t *testing.T) Definition {
	t.Helper()
	definition := testDefinition(t)
	definition.Recipes[0].Criteria = criteria.Criteria{Category: "os", Role: "any", OS: "ubuntu", Arch: "amd64"}
	definition.Outputs[0].Format = OutputDiskImage
	definition.Outputs[0].Components = []string{"nke-node-non-fde"}
	return definition
}

func TestDiskImageOutputSchemaAndIdentity(t *testing.T) {
	definition := diskDefinition(t)
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid disk output rejected: %v", err)
	}
	diskDigest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	executable := definition
	executable.Outputs = append([]DefinitionOutput(nil), definition.Outputs...)
	executable.Outputs[0].Format = OutputExecutable
	executableDigest, err := executable.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if diskDigest == executableDigest {
		t.Fatal("disk output format did not affect Definition identity")
	}

	tests := []struct {
		name string
		edit func(*Definition)
	}{
		{"no component", func(d *Definition) { d.Outputs[0].Components = nil }},
		{"two components", func(d *Definition) { d.Outputs[0].Components = []string{"a", "b"} }},
		{"non-os recipe", func(d *Definition) { d.Recipes[0].Criteria.Category = "kubernetes" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := diskDefinition(t)
			tt.edit(&invalid)
			assertCode(t, invalid.Validate(), nkderr.CodeInvalidCatalog)
		})
	}
}

func TestDiskImageCompileFailsBeforeCatalogLowering(t *testing.T) {
	definition := diskDefinition(t)
	definition.Catalog.Base = "missing-catalog"
	_, err := Compile(context.Background(), "missing-definition.yaml", definition,
		"environment.yaml", testEnvironment(), "test")
	assertCode(t, err, nkderr.CodeEngineCapability)
}
