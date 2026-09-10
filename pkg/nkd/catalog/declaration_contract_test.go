package catalog

import (
	"errors"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestCatalogRejectsAmbiguousRecipeMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Catalog)
	}{
		{"blank component name", func(c *Catalog) { c.Components[" "] = goodComponent() }},
		{"padded recipe name", func(c *Catalog) { c.Recipes[0].ID = " r " }},
		{"duplicate recipe", func(c *Catalog) { c.Recipes = append(c.Recipes, c.Recipes[0]) }},
		{"blank component reference", func(c *Catalog) { c.Recipes[0].Components = []string{" "} }},
		{"duplicate component reference", func(c *Catalog) { c.Recipes[0].Components = []string{"x", "x"} }},
		{"unknown component reference", func(c *Catalog) { c.Recipes[0].Components = []string{"missing"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := base(goodComponent())
			tc.edit(cat)
			assertCatalogContractCode(t, cat.Validate(), e.CodeInvalidCatalog)
		})
	}
}

func TestCatalogRejectsUnusableNixPinsAndDownloadLocations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*FlakePin)
		code e.Code
	}{
		{"complete pin", func(*FlakePin) {}, ""},
		{"blank reference", func(f *FlakePin) { f.Ref = " " }, e.CodeUnpinnedSource},
		{"blank output", func(f *FlakePin) { f.Output = " " }, e.CodeInvalidCatalog},
		{"wrong image scheme", func(f *FlakePin) { f.Image = "file:///builder" }, e.CodeInvalidCatalog},
		{"mutable builder image", func(f *FlakePin) { f.Image = "nix:latest" }, e.CodeTagToolchain},
		{"pinned builder image", func(f *FlakePin) { f.Image = goodImage }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := goodComponent()
			component.BuilderType = "nix"
			component.Flake = &FlakePin{Ref: "github:example/project?rev=" + goodCommit, LockDigest: goodArtifactDigest, Output: "packages.x86_64-linux.default"}
			tc.edit(component.Flake)
			err := base(component).Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, tc.code)
			}
		})
	}
	for _, location := range []string{"http://example.test/tool", "file:///tool", "example.test/tool"} {
		t.Run(location, func(t *testing.T) {
			component := goodPrebuilt()
			component.Prebuilt.URL = location
			assertCatalogContractCode(t, base(component).Validate(), e.CodeInvalidCatalog)
		})
	}
}

func assertCatalogContractCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v; want %s", err, want)
	}
}
