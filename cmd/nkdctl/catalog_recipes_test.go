package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/urfave/cli/v2"
)

func TestCatalogValidateReportsLayeredShapeInsteadOfZeroRecipes(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "validate", "--data", layeredCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "layered catalog: 3 category recipe pattern(s), 1 base, 4 overlay(s), 1 mixin(s), 7 component(s)"
	if !strings.Contains(output, want) {
		t.Fatalf("catalog validation summary omitted layered recipe shape:\n%s", output)
	}
	if strings.Contains(output, "— 0 recipe(s)") {
		t.Fatalf("catalog validation retained the misleading flat-only count:\n%s", output)
	}
}

func TestCatalogRecipesIncludesExactAICRSelectionAlongsideOrdinaryCategories(t *testing.T) {
	table, err := runCatalogCommand(t, "catalog", "recipes", "--data", layeredCatalogPath(t),
		"--k8s", "1.32.0", "--os", "ubuntu", "--arch", "amd64",
		"--service", "eks", "--accelerator", "h100", "--intent", "training")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, "AICR activation closure") {
		t.Fatalf("activation-only recipe was presented as an empty recipe:\n%s", table)
	}
	output, err := runCatalogCommand(t, "catalog", "recipes", "--data", layeredCatalogPath(t),
		"--k8s", "1.32.0", "--os", "ubuntu", "--arch", "amd64",
		"--service", "eks", "--accelerator", "h100", "--intent", "training", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []recipe.ResolvableRecipe
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decoding AICR recipe enumeration: %v\n%s", err, output)
	}
	if len(got) != 3 || got[0].Criteria.Category != "accelerated-runtime" ||
		got[0].AICR == nil || got[0].AICR.Accelerator != "h100" ||
		got[0].AICRProfileIdentity != "h100-eks-ubuntu-training" {
		t.Fatalf("AICR-aware resolved recipes = %+v", got)
	}
}

func TestCatalogRecipesEnumeratesConcreteResolvedRecipes(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "recipes", "--data", layeredCatalogPath(t),
		"--k8s", "1.32.0", "--os", "ubuntu", "--arch", "amd64", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []recipe.ResolvableRecipe
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decoding recipe enumeration: %v\n%s", err, output)
	}
	if len(got) != 2 || got[0].RecipeID != "kubernetes-1.32.0-ubuntu-amd64" ||
		got[1].RecipeID != "platform-services-1.32.0-ubuntu-amd64" {
		t.Fatalf("resolved recipes = %+v", got)
	}
	for _, item := range got {
		if !strings.HasPrefix(item.RecipeDigest, "sha256:") || len(item.Components) == 0 {
			t.Fatalf("enumeration omitted identity or components: %+v", item)
		}
	}
}

func TestCatalogListShowsRecipeSelectionCriteria(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "list", "--data", layeredCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "category=kubernetes") || !strings.Contains(output, "os=ubuntu") ||
		strings.Contains(output, "role=any clusterType=any") {
		t.Fatalf("catalog list obscures recipe-selection criteria:\n%s", output)
	}
}

func runCatalogCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdCatalog()}}
	runErr := app.Run(append([]string{"nkdctl"}, args...))
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	raw, readErr := io.ReadAll(readEnd)
	if closeErr := readEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(raw), runErr
}

func layeredCatalogPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "examples", "layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}
