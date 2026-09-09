package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestResolveNodesOutputPreservesIdentityFrame(t *testing.T) {
	queryArgs := []string{
		"--category", "kubernetes", "--role", "worker",
		"--k8s", "1.32.0", "--arch", "amd64",
		"--data", filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"),
	}
	app := &cli.App{Commands: []*cli.Command{cmdRecipe()}}
	nodes, err := runCLIWithCapturedStdout(t, app,
		append([]string{"nkdctl", "resolve"}, append(queryArgs, "-o", "nodes")...)...)
	if err != nil {
		t.Fatalf("resolve -o nodes failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(nodes, &decoded); err != nil {
		t.Fatalf("nodes output is not JSON: %v\n%s", err, nodes)
	}
	if _, ok := decoded["nodes"].([]any); !ok {
		t.Fatalf("nodes output omitted the full execution graph: %s", nodes)
	}
	criteria, _ := decoded["criteria"].(map[string]any)
	// nodes.json is the execution document for a role-invariant recipeDigest, so it must
	// NOT carry the composition role axis: two role-SKUs of one category share a
	// recipeDigest and must therefore share byte-identical nodes.json (recipe.Compile
	// clears role/clusterType from the identity frame).
	if _, present := criteria["role"]; present {
		t.Fatalf("nodes output must not carry the composition role axis: %#v", criteria)
	}
	if criteria["category"] != "kubernetes" {
		t.Fatalf("nodes output dropped the recipe identity frame: %#v", criteria)
	}

}

func runCLIWithCapturedStdout(t *testing.T, app *cli.App, args ...string) ([]byte, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = output
	runErr := app.Run(args)
	os.Stdout = original
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return raw, runErr
}
