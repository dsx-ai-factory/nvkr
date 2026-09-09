package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func TestPublicationRootEnclosesAPTRepositoryAsOCIArtifact(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("inputs", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("inputs/tool", []byte("tool-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Recipes: []RecipePlan{
		{
			Name: "node",
			Graph: &ir.Graph{Nodes: []ir.Node{
				{
					ID: "build-tool", Kind: ir.KindBuild, Component: "tool", Version: "1.0.0",
					Platform: "linux/amd64", Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1733939955"}},
				},
			}},
		},
	}}
	artifacts := map[string]Artifact{
		"artifact/node/tool": {ID: "artifact/node/tool", Name: "tool", Kind: "host-package", Path: "inputs/tool", RecipeTarget: "node", Component: "tool"},
		"package/worker-apt": {ID: "package/worker-apt", Name: "worker-apt", Kind: PackageAPT, MediaType: packageMediaType(PackageAPT), Path: "outputs/apt"},
		"package/root":       {ID: "package/root", Name: "root", Kind: PackageOCI, MediaType: packageMediaType(PackageOCI), Path: "outputs/root"},
	}
	aptNode := Node{ID: "package/worker-apt", Kind: NodePackage, PackageKind: PackageAPT,
		Distribution: "stable", PackageComponent: "main", Inputs: []string{"artifact/node/tool"}, Outputs: []string{"package/worker-apt"}}
	rootNode := Node{ID: "package/root", Kind: NodePackage, PackageKind: PackageOCI,
		Inputs: []string{"package/worker-apt"}, Outputs: []string{"package/root"}}
	plan.Graph = ArtifactGraph{Nodes: []Node{aptNode, rootNode}, Artifacts: []Artifact{
		artifacts["artifact/node/tool"], artifacts["package/worker-apt"], artifacts["package/root"],
	}}
	if err := executePackage(context.Background(), plan, aptNode, artifacts); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(context.Background(), plan, rootNode, artifacts); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(context.Background(), filepath.FromSlash("outputs/root"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Root.Objects) != 1 || inspection.Root.Objects[0].Kind != PackageAPT ||
		inspection.Root.Objects[0].Name != "worker-apt" {
		t.Fatalf("publication objects = %+v", inspection.Root.Objects)
	}
}

func TestHelmPackageIsStableAcrossWorkspaceRelocationAndReclaimsCarrier(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	canonicalInput := filepath.FromSlash(".nkd/factory/aicr/gpu")
	canonicalOutput := filepath.FromSlash(".nkd/factory/packages/gpu-runtime")
	if err := os.MkdirAll(canonicalInput, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalInput, "recipe.yaml"), []byte("apiVersion: aicr.nvidia.com/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "package/gpu", Kind: NodePackage, PackageKind: PackageHelm,
		Inputs: []string{"artifact/aicr"}, Outputs: []string{"package/gpu"}}
	plannedArtifacts := []Artifact{
		{ID: "artifact/aicr", Name: "aicr", Kind: "aicr-bundle", Path: filepath.ToSlash(canonicalInput)},
		{ID: "package/gpu", Name: "gpu-runtime", Kind: PackageHelm,
			MediaType: packageMediaType(PackageHelm), Path: filepath.ToSlash(canonicalOutput), Producer: node.ID},
	}
	plan := &Plan{Graph: ArtifactGraph{Nodes: []Node{node}, Artifacts: plannedArtifacts}}
	artifacts := map[string]Artifact{"artifact/aicr": plannedArtifacts[0], "package/gpu": plannedArtifacts[1]}
	carrier := filepath.FromSlash(".nkd/factory/packages/.gpu-runtime.aicr.tar")
	if err := os.MkdirAll(filepath.Dir(canonicalOutput), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(carrier, []byte("interrupted prior run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(context.Background(), plan, node, artifacts); err != nil {
		t.Fatal(err)
	}
	first, err := delivery.Inspect(context.Background(), canonicalOutput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(carrier); !os.IsNotExist(err) {
		t.Fatalf("retained internal carrier: %v", err)
	}

	relocatedRoot := filepath.FromSlash(".nkd/runs/attempt")
	if err := os.MkdirAll(filepath.Dir(relocatedRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.FromSlash(".nkd/factory"), relocatedRoot); err != nil {
		t.Fatal(err)
	}
	relocatedInput := filepath.Join(relocatedRoot, "aicr", "gpu")
	relocatedOutput := filepath.Join(relocatedRoot, "packages", "gpu-runtime")
	if err := os.RemoveAll(relocatedOutput); err != nil {
		t.Fatal(err)
	}
	artifacts["artifact/aicr"] = Artifact{ID: "artifact/aicr", Name: "aicr", Kind: "aicr-bundle", Path: relocatedInput}
	artifacts["package/gpu"] = Artifact{ID: "package/gpu", Name: "gpu-runtime", Kind: PackageHelm,
		MediaType: packageMediaType(PackageHelm), Path: relocatedOutput, Producer: node.ID}
	if err := executePackage(context.Background(), plan, node, artifacts); err != nil {
		t.Fatal(err)
	}
	second, err := delivery.Inspect(context.Background(), relocatedOutput)
	if err != nil {
		t.Fatal(err)
	}
	if first.RootDigest != second.RootDigest {
		t.Fatalf("Helm payload changed across workspace relocation: %s != %s", first.RootDigest, second.RootDigest)
	}
}
