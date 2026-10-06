package definition

import (
	"errors"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func indexDefinitionFixture() Definition {
	return Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "fixture"}, Catalog: Catalog{Base: "catalog"}, Outputs: []DefinitionOutput{{Name: "cwe", Platforms: []string{"linux/amd64", "linux/arm64"}, Format: OutputOCI, Components: []string{"controller"}}}}
}

func TestExplicitImagePlatformAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Definition)
		valid  bool
	}{
		{"exact", func(*Definition) {}, true},
		{"finite subset", func(d *Definition) { d.Outputs[0].Platforms = d.Outputs[0].Platforms[:1] }, true},
		{"single selection", func(d *Definition) { d.Outputs[0].Platforms = nil; d.Outputs[0].Platform = "linux/arm64" }, true},
		{"explicit empty set", func(d *Definition) { d.Outputs[0].Platforms = []string{}; d.Outputs[0].Platform = "linux/amd64" }, false},
		{"mixed", func(d *Definition) { d.Outputs[0].Platform = "linux/amd64" }, false},
		{"empty", func(d *Definition) { d.Outputs[0].Platforms = nil }, false},
		{"unordered", func(d *Definition) { d.Outputs[0].Platforms = []string{"linux/arm64", "linux/amd64"} }, false},
		{"duplicate", func(d *Definition) { d.Outputs[0].Platforms = []string{"linux/amd64", "linux/amd64"} }, false},
		{"unsupported", func(d *Definition) { d.Outputs[0].Platforms = []string{"linux/riscv64"} }, false},
		{"executable aggregate", func(d *Definition) { d.Outputs[0].Format = OutputExecutable }, false},
		{"multiple components", func(d *Definition) { d.Outputs[0].Components = []string{"a", "b"} }, false},
		{"legacy schema", func(d *Definition) { d.APIVersion = "nkd.dev/definition/v4" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := indexDefinitionFixture()
			tc.mutate(&d)
			err := d.Validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestImagePlatformRequirementsAreOwnedAndIdentityBound(t *testing.T) {
	d := indexDefinitionFixture()
	before, err := d.Digest()
	if err != nil {
		t.Fatal(err)
	}
	clone := d.Clone()
	clone.Outputs[0].Platforms[0] = "changed"
	targets := d.Outputs[0].TargetPlatforms()
	targets[0] = "changed"
	if d.Outputs[0].Platforms[0] != "linux/amd64" {
		t.Fatal("aliased platform selection")
	}
	d.Outputs[0].Platforms = d.Outputs[0].Platforms[:1]
	after, err := d.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("membership did not change Definition identity")
	}
}

func TestHelmReferenceRequiresImagePlatformCoverage(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			d := indexDefinitionFixture()
			d.Outputs = append([]DefinitionOutput{{Name: "chart", Platform: platform, Format: OutputHelm, Components: []string{"chart"}, Helm: &HelmOutput{Images: map[string]string{"controller": "cwe"}}}}, d.Outputs...)
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			d.Outputs[1].Platforms = []string{"linux/amd64"}
			err := d.Validate()
			if platform == "linux/arm64" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
