package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCompileDocumentsResolvesCatalogRelativeToDefinition(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var recipeDigest string
	for _, tc := range []struct {
		name                        string
		absolute, override, missing bool
	}{
		{name: "relative base"},
		{name: "absolute base", absolute: true},
		{name: "target override", override: true},
		{name: "missing target catalog", override: true, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogPath := "../testdata/catalogs/kubernetes-layered"
			if tc.absolute {
				catalogPath = filepath.Join(repo, "testdata/catalogs/kubernetes-layered")
			}
			definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind, Metadata: Metadata{Name: "documents"}, Catalog: CatalogStack{Base: catalogPath},
				UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161", UtilityPlatform: "linux/amd64",
				Recipes:  []RecipeTarget{{Name: "worker", Criteria: criteria.Criteria{Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64"}}},
				Packages: []PackageSpec{{Name: "tools", Kind: PackageBinaryBundle, Inputs: []string{"worker/*"}}}}
			if tc.override {
				definition.Recipes[0].Catalog = &CatalogStack{Base: catalogPath}
			}
			if tc.missing {
				definition.Recipes[0].Catalog.Base = "missing"
			}
			environment := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind, Metadata: Metadata{Name: "local"}, RunsOn: "ubuntu-24.04"}
			definitionPath := filepath.Join("definitions", "fixture.yaml")
			plan, err := CompileDocuments(t.Context(), definitionPath, definition, "environment.yaml", environment, "test")
			if tc.missing {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				if plan != nil {
					t.Fatal("failed compilation returned plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.DefinitionPath != filepath.ToSlash(definitionPath) || plan.EnvironmentPath != "environment.yaml" {
				t.Fatalf("plan lost authored paths: %+v", plan)
			}
			if len(plan.Recipes) != 1 {
				t.Fatalf("wrong recipe set: %+v", plan.Recipes)
			}
			if recipeDigest == "" {
				recipeDigest = plan.Recipes[0].RecipeDigest
			}
			if plan.Recipes[0].RecipeDigest != recipeDigest {
				t.Fatal("catalog transport path changed recipe identity")
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocumentCatalogStackBindsOverlayInputAndOutput(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["crictl"]
	component.Tag = "v99.0.0"
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "version-change", RequiresCatalogDigest: inputDigest,
		Components: map[string]catalog.ComponentReplacement{"crictl": {Component: &component}}}
	expected, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                        string
		absolute, missing, wrongPin bool
	}{
		{name: "relative overlay"},
		{name: "absolute overlay", absolute: true},
		{name: "missing overlay", missing: true},
		{name: "wrong predecessor", wrongPin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			copyOverlay := overlay
			if tc.wrongPin {
				copyOverlay.RequiresCatalogDigest = "sha256:" + strings.Repeat("a", 64)
			}
			raw, err := canonical.Marshal(copyOverlay)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "overlay.json.yaml")
			if !tc.missing {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			selected := filepath.Base(path)
			if tc.absolute {
				selected = path
			}
			stack, err := loadFactoryCatalogStack(t.Context(), filepath.Join(root, "definition.yaml"), CatalogStack{Base: filepath.Join(repo, "testdata/catalogs/kubernetes-layered"), Overlays: []string{selected}})
			if tc.missing || tc.wrongPin {
				want := e.CodeInvalidCatalog
				if tc.wrongPin {
					want = e.CodeUnverifiedRef
				}
				assertFactoryErrorCode(t, err, want)
				if stack != nil {
					t.Fatal("invalid overlay returned stack")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stack.Digest != expected.Digest || stack.Digest == inputDigest || len(stack.Applied) != 1 || stack.Applied[0].InputDigest != inputDigest || stack.Applied[0].OutputDigest != stack.Digest {
				t.Fatalf("overlay provenance lost exact state: %+v", stack)
			}
			if stack.Catalog.Components["crictl"].Tag != "v99.0.0" {
				t.Fatal("overlay replacement was not applied")
			}
		})
	}
}
